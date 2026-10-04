// Package owner mints opaque serializer identities. Go's internal rule limits
// minting to adapters; workspace renderers and application packages can only
// receive handles from provider serializer leaves.
package owner

// Owner is a comparable immutable handle. Its zero value owns nothing.
type Owner struct{ identity *identity }
type identity struct{ name string }

// New mints a distinct owner; provider leaves keep one immutable instance.
func New(name string) Owner {
	if name == "" {
		return Owner{}
	}
	return Owner{identity: &identity{name: name}}
}
func (o Owner) Valid() bool { return o.identity != nil }
