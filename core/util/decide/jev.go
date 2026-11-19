package decide

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// encodeJEV writes JEV's System-1 body by hand: JSON objects from Go maps lose
// order, and JEV reads the order of questions, options and levels.
func encodeJEV(req Request) ([]byte, error) {
	if req.Model == "" {
		return nil, errors.New("decide: the jev dialect needs Request.Model (the gateway routes on it)")
	}
	state, err := json.Marshal(req.State)
	if err != nil {
		return nil, fmt.Errorf("decide: encode state: %w", err)
	}
	model, _ := json.Marshal(req.Model)
	var b bytes.Buffer
	b.WriteString(`{"state":`)
	b.Write(state)
	b.WriteString(`,"model":`)
	b.Write(model)
	b.WriteString(`,"questions":{`)
	for i, q := range req.Questions {
		if i > 0 {
			b.WriteByte(',')
		}
		key, _ := json.Marshal(q.Key)
		b.Write(key)
		b.WriteByte(':')
		instr, _ := json.Marshal(q.Instructions)
		fmt.Fprintf(&b, `{"type":%q,"instructions":%s`, string(q.Type), instr)
		switch q.Type {
		case Noul:
			if q.True != "" || q.False != "" {
				b.WriteString(`,"criteria":{`)
				sep := ""
				if q.True != "" {
					t, _ := json.Marshal(q.True)
					b.WriteString(`"true":`)
					b.Write(t)
					sep = ","
				}
				if q.False != "" {
					f, _ := json.Marshal(q.False)
					b.WriteString(sep + `"false":`)
					b.Write(f)
				}
				b.WriteByte('}')
			}
		case Choice:
			b.WriteString(`,"criteria":{`)
			for j, o := range q.Options {
				if j > 0 {
					b.WriteByte(',')
				}
				k, _ := json.Marshal(o.Key)
				b.Write(k)
				b.WriteByte(':')
				if o.Description == "" {
					b.WriteString("null")
				} else {
					d, _ := json.Marshal(o.Description)
					b.Write(d)
				}
			}
			b.WriteByte('}')
		case Score:
			levels, _ := json.Marshal(q.Levels)
			b.WriteString(`,"criteria":`)
			b.Write(levels)
		}
		b.WriteByte('}')
	}
	b.WriteString(`}}`)
	return b.Bytes(), nil
}

type jevResponse struct {
	Model   string `json:"model"`
	Answers map[string]struct {
		Type          QuestionType       `json:"type"`
		Choice        string             `json:"choice"`
		Noul          *float64           `json:"noul"`
		Score         *float64           `json:"score"`
		Confidence    *float64           `json:"confidence"`
		Probabilities map[string]float64 `json:"probabilities"`
	} `json:"answers"`
	Usage Usage `json:"usage"`
}

func decodeJEV(req Request, body []byte) (Response, error) {
	var w jevResponse
	if err := json.Unmarshal(body, &w); err != nil {
		return Response{}, fmt.Errorf("decide: bad response: %w", err)
	}
	answers := make(map[string]Answer, len(w.Answers))
	for k, a := range w.Answers {
		ans := Answer{Type: a.Type, Choice: a.Choice, Probabilities: a.Probabilities}
		if a.Noul != nil {
			ans.Noul = *a.Noul
		} else if a.Type == Noul {
			return Response{}, fmt.Errorf("decide: question %q: noul answer has no value", k)
		}
		if a.Score != nil {
			ans.Score = *a.Score
		} else if a.Type == Score {
			return Response{}, fmt.Errorf("decide: question %q: score answer has no value", k)
		}
		if a.Confidence != nil {
			ans.Confidence = *a.Confidence
		}
		answers[k] = ans
	}
	return check(req, Response{Provider: "jev", Model: w.Model, Usage: w.Usage}, answers)
}
