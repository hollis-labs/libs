package quota_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/substrate/llm-core/quota"
	"github.com/hollis-labs/substrate/llm-core/quota/quotatest"
)

func TestMemoryStoreConformance(t *testing.T) {
	newHarness := func(t *testing.T) quotatest.Harness {
		return quotatest.Harness{Store: quota.NewMemoryStore()}
	}
	quotatest.RunStore(t, newHarness)
	quotatest.RunLimiter(t, newHarness)
}

func TestSeededStoreConformance(t *testing.T) {
	newHarness := func(t *testing.T) quotatest.Harness {
		return quotatest.Harness{Store: quota.NewSeededStore(nil, 0, nil)}
	}
	quotatest.RunStore(t, newHarness)
	quotatest.RunLimiter(t, newHarness)
}

func TestSQLStoreConformance(t *testing.T) {
	newHarness := func(t *testing.T) quotatest.Harness {
		db, table := openFakeDB(t)
		return quotatest.Harness{
			Store: newSQLStore(db),
			Add: func(_ testing.TB, c quota.Counter, e quota.Entry) {
				table.insert(fakeRow{key: c.Key, unit: string(c.Unit), at: e.At, amount: e.Amount})
			},
		}
	}
	quotatest.RunStore(t, newHarness)
	quotatest.RunLimiter(t, newHarness)
}

func newSQLStore(db *sql.DB) *quota.SQLStore {
	return &quota.SQLStore{
		DB:           db,
		Table:        "usage_events",
		TimeColumn:   "at",
		AmountColumn: "amount",
		Filter: func(c quota.Counter) (string, []any) {
			return "key = ? AND unit = ?", []any{c.Key, string(c.Unit)}
		},
	}
}

func TestSeededStoreSeedsFromHistory(t *testing.T) {
	clk := quotatest.NewClock(quotatest.Epoch)
	lim := quota.Limit{Key: "rule/alice/write", Unit: "calls", Window: quota.Rolling(time.Hour), Max: 3}
	c := lim.Counter()
	var calls int
	var since []time.Time
	history := []quota.SeedEntry{
		{Counter: c, Entry: quota.Entry{At: quotatest.Epoch.Add(-30 * time.Minute), Amount: 1}},
		{Counter: c, Entry: quota.Entry{At: quotatest.Epoch.Add(-10 * time.Minute), Amount: 1}},
	}
	seed := func(_ context.Context, s time.Time) ([]quota.SeedEntry, error) {
		calls++
		since = append(since, s)
		return history, nil
	}
	store := quota.NewSeededStore(seed, 24*time.Hour, clk)
	l := quota.New(store, quota.WithClock(clk))

	// A restarted process counts what already ran.
	decide(t, l, lim, 1, quota.Decision{Allowed: true, Remaining: 1})
	if calls != 1 || !since[0].Equal(quotatest.Epoch.Add(-24*time.Hour)) {
		t.Fatalf("seed calls = %d, since = %v", calls, since)
	}
	r, _, err := l.Reserve(ctx, lim, 1)
	if err != nil || r == nil {
		t.Fatal(err)
	}
	if err := r.Commit(ctx, 1); err != nil {
		t.Fatal(err)
	}
	decide(t, l, lim, 1, quota.Decision{Remaining: 0, RetryAfter: 30 * time.Minute, Reason: quota.ReasonExhausted})
	if calls != 1 {
		t.Fatalf("seeded %d times; want once until Reseed", calls)
	}

	// The application recorded the call in its own log; a reseed reads it back
	// instead of the in-memory copy.
	history = append(history, quota.SeedEntry{Counter: c, Entry: quota.Entry{At: quotatest.Epoch, Amount: 1}})
	store.Reseed()
	decide(t, l, lim, 1, quota.Decision{Remaining: 0, RetryAfter: 30 * time.Minute, Reason: quota.ReasonExhausted})
	if calls != 2 {
		t.Fatalf("seed calls = %d; want 2 after Reseed", calls)
	}
}

func TestSeededStoreRetriesAFailedSeed(t *testing.T) {
	boom := errors.New("audit log unreadable")
	fail := true
	seed := func(context.Context, time.Time) ([]quota.SeedEntry, error) {
		if fail {
			return nil, boom
		}
		return nil, nil
	}
	store := quota.NewSeededStore(seed, time.Hour, nil)
	c := quota.Counter{Key: "k", Unit: "u", Window: "w"}
	if _, err := store.Sum(ctx, c, time.Time{}, time.Now()); !errors.Is(err, boom) {
		t.Fatalf("Sum = %v; want the seed error", err)
	}
	if err := store.Record(ctx, c, quota.Entry{At: time.Now(), Amount: 1}); !errors.Is(err, boom) {
		t.Fatalf("Record = %v; want the seed error", err)
	}
	fail = false
	if n, err := store.Sum(ctx, c, time.Time{}, time.Now().Add(time.Hour)); err != nil || n != 0 {
		t.Fatalf("Sum after recovery = %d, %v", n, err)
	}
	bad := quota.NewSeededStore(func(context.Context, time.Time) ([]quota.SeedEntry, error) {
		return []quota.SeedEntry{{Counter: c, Entry: quota.Entry{Amount: -1}}}, nil
	}, time.Hour, nil)
	if _, err := bad.Sum(ctx, c, time.Time{}, time.Now()); !errors.Is(err, quota.ErrNegativeAmount) {
		t.Fatalf("Sum with a negative seed entry = %v; want ErrNegativeAmount", err)
	}
}

func TestMemoryStoreForget(t *testing.T) {
	m := quota.NewMemoryStore()
	c := quota.Counter{Key: "k", Unit: "u", Window: "w"}
	at := func(h int) time.Time { return quotatest.Epoch.Add(time.Duration(h) * time.Hour) }
	for h := range 4 {
		if err := m.Record(ctx, c, quota.Entry{At: at(h), Amount: 1}); err != nil {
			t.Fatal(err)
		}
	}
	m.Forget(at(2))
	if n, _ := m.Sum(ctx, c, at(0), at(10)); n != 2 {
		t.Fatalf("Sum after Forget = %d; want 2", n)
	}
	m.Forget(at(10))
	if n, _ := m.Sum(ctx, c, at(0), at(10)); n != 0 {
		t.Fatalf("Sum after forgetting everything = %d; want 0", n)
	}
	if err := m.Record(ctx, c, quota.Entry{At: at(0), Amount: -1}); !errors.Is(err, quota.ErrNegativeAmount) {
		t.Fatalf("Record(-1) = %v", err)
	}
}

func TestSQLStoreQueryShape(t *testing.T) {
	db, table := openFakeDB(t)
	s := newSQLStore(db)
	c := quota.Counter{Key: "k", Unit: "tokens", Window: "w"}
	if _, err := s.Sum(ctx, c, quotatest.Epoch, quotatest.Epoch.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	want := "SELECT COALESCE(SUM(amount), 0) FROM usage_events WHERE (key = ? AND unit = ?) AND at >= ? AND at < ?"
	if got := table.lastQuery(); got != want {
		t.Fatalf("query\n got %s\nwant %s", got, want)
	}
	s.Placeholder = quota.DollarPlaceholder
	if _, err := s.Entries(ctx, c, quotatest.Epoch, quotatest.Epoch.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	want = "SELECT at, amount FROM usage_events WHERE (key = $1 AND unit = $2) AND at >= $3 AND at < $4 ORDER BY at"
	if got := table.lastQuery(); got != want {
		t.Fatalf("query\n got %s\nwant %s", got, want)
	}
}

func TestSQLStoreRefusesBadConfig(t *testing.T) {
	db, _ := openFakeDB(t)
	c := quota.Counter{Key: "k", Unit: "u", Window: "w"}
	for name, mutate := range map[string]func(*quota.SQLStore){
		"table injection":  func(s *quota.SQLStore) { s.Table = "usage_events; DROP TABLE x" },
		"column injection": func(s *quota.SQLStore) { s.AmountColumn = "amount) --" },
		"no db":            func(s *quota.SQLStore) { s.DB = nil },
		"no filter":        func(s *quota.SQLStore) { s.Filter = nil },
		"empty filter":     func(s *quota.SQLStore) { s.Filter = func(quota.Counter) (string, []any) { return " ", nil } },
		"argument count": func(s *quota.SQLStore) {
			s.Filter = func(quota.Counter) (string, []any) { return "key = ?", nil }
		},
	} {
		s := newSQLStore(db)
		mutate(s)
		if _, err := s.Sum(ctx, c, time.Time{}, time.Now()); err == nil {
			t.Errorf("%s: Sum succeeded", name)
		}
		if _, err := s.Entries(ctx, c, time.Time{}, time.Now()); err == nil {
			t.Errorf("%s: Entries succeeded", name)
		}
	}
	ok := newSQLStore(db)
	ok.Table = "app.usage_events"
	if _, err := ok.Sum(ctx, c, time.Time{}, time.Now()); err != nil {
		t.Errorf("schema-qualified table refused: %v", err)
	}
}

func TestSQLStoreScansTextTimes(t *testing.T) {
	db, table := openFakeDB(t)
	table.textTimes = true
	s := newSQLStore(db)
	c := quota.Counter{Key: "k", Unit: "u", Window: "w"}
	at := quotatest.Epoch.Add(90 * time.Second)
	table.insert(fakeRow{key: "k", unit: "u", at: at, amount: 4})
	es, err := s.Entries(ctx, c, quotatest.Epoch, quotatest.Epoch.Add(time.Hour))
	if err != nil || len(es) != 1 || !es[0].At.Equal(at) || es[0].Amount != 4 {
		t.Fatalf("Entries = %v, %v", es, err)
	}
}

// The fake driver below stands in for a real database so that the module
// keeps no database driver dependency. It understands exactly the two query
// shapes SQLStore issues with the filter "key = ? AND unit = ?".

type fakeRow struct {
	key, unit string
	at        time.Time
	amount    int64
}

type fakeTable struct {
	mu        sync.Mutex
	rows      []fakeRow
	queries   []string
	textTimes bool
}

func (f *fakeTable) insert(r fakeRow) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, r)
}

func (f *fakeTable) lastQuery() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queries) == 0 {
		return ""
	}
	return f.queries[len(f.queries)-1]
}

func openFakeDB(t *testing.T) (*sql.DB, *fakeTable) {
	table := &fakeTable{}
	db := sql.OpenDB(fakeConnector{table})
	t.Cleanup(func() { _ = db.Close() })
	return db, table
}

type fakeConnector struct{ table *fakeTable }

func (c fakeConnector) Connect(context.Context) (driver.Conn, error) { return fakeConn(c), nil }
func (c fakeConnector) Driver() driver.Driver                        { return fakeDriver{} }

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use the connector") }

type fakeConn struct{ table *fakeTable }

func (fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (fakeConn) Close() error                        { return nil }
func (fakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("not supported") }

func (c fakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	f := c.table
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, query)
	if len(args) != 4 {
		return nil, fmt.Errorf("fake: want 4 arguments, got %d", len(args))
	}
	key, _ := args[0].Value.(string)
	unit, _ := args[1].Value.(string)
	from, _ := args[2].Value.(time.Time)
	to, _ := args[3].Value.(time.Time)
	var match []fakeRow
	for _, r := range f.rows {
		if r.key == key && r.unit == unit && !r.at.Before(from) && r.at.Before(to) {
			match = append(match, r)
		}
	}
	if strings.Contains(query, "SUM(") {
		var n int64
		for _, r := range match {
			n += r.amount
		}
		return &fakeRows{cols: []string{"sum"}, data: [][]driver.Value{{n}}}, nil
	}
	sort.SliceStable(match, func(i, j int) bool { return match[i].at.Before(match[j].at) })
	out := &fakeRows{cols: []string{"at", "amount"}}
	for _, r := range match {
		var at driver.Value = r.at
		if f.textTimes {
			at = r.at.Format(time.RFC3339Nano)
		}
		out.data = append(out.data, []driver.Value{at, r.amount})
	}
	return out, nil
}

type fakeRows struct {
	cols []string
	data [][]driver.Value
	i    int
}

func (r *fakeRows) Columns() []string { return r.cols }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.i >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.i])
	r.i++
	return nil
}
