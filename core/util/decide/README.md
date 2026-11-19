# decide

Asks a decision provider structured questions about a piece of state and gets probabilities back: `noul` (the probability a statement is true), `choice` (one of a fixed set) or `score` (ordered levels). The provider never writes text. The caller owns the questions, the thresholds, and what is done with an answer.

## Status

**Pre-release.** No compatibility guarantees until the first tagged release of its module.

## Usage

```go
c := decide.NewClient("http://127.0.0.1:8080", token, "my-app") // or unix:///path/to/gateway.sock
resp, err := c.Decide(ctx, decide.Request{
	State:   map[string]any{"text": decide.Scrub(text)},
	Purpose: "classify", // required: spend is accounted per purpose
	Model:   "jev-1.13.0",
	Questions: []decide.Question{{Key: "act", Type: decide.Choice, Instructions: "What does the message ask for?",
		Options: []decide.Option{{Key: "task", Description: "do something"}, {Key: "question", Description: "ask something"}}}},
})
a := resp.Answers["act"] // a.Choice, a.Probabilities, a.Confidence
```

Confidence measures how concentrated a distribution is. It is not the chance the answer is right: set thresholds by evaluating on your own data.
