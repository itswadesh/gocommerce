package aigateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// summaryTask asks for a one-line summary; build decides what it sends.
func summaryTask(build func(input json.RawMessage) []Message) Task {
	return Task{
		Name: "test.summarise", Version: 1, System: "Summarise.",
		Schema:         json.RawMessage(`{"type":"object","properties":{"summary":{"type":"string"}},"required":["summary"],"additionalProperties":false}`),
		MaxInputTokens: 4000, MaxOutputTokens: 500, Ladder: []Tier{TierSmall},
		Build: func(ctx context.Context, input json.RawMessage, p Policy) (Request, []SourceRef, error) {
			return Request{Messages: build(input)}, nil, nil
		},
	}
}

const summaryAnswer = `{"summary": "Fine."}`

// The task's own words are held to the policy as its data is: an address a
// task wrote into Text is not a way round the switch.
func TestAMessagesOwnWordsAreHeldToThePolicy(t *testing.T) {
	task := summaryTask(func(json.RawMessage) []Message {
		return []Message{{Role: "user", Text: "Reply to ana@example.com, or call +44 20 7946 0958."}}
	})
	app, m, s := newGateway(t, reply(http.StatusOK, answer(smallID, summaryAnswer, smallUsage)), task)
	ctx := context.Background()
	configure(t, app, ctx, nil)

	if _, err := m.Run(ctx, "test.summarise", nil); err != nil {
		t.Fatal(err)
	}
	if body := string(s.last(t).Body); strings.Contains(body, "ana@example.com") || strings.Contains(body, "7946 0958") {
		t.Errorf("contact details left in a message's own words: %s", body)
	}
	configure(t, app, ctx, map[string]any{"share_contact_details": true})
	if _, err := m.Run(ctx, "test.summarise", nil); err != nil {
		t.Fatal(err)
	}
	if body := string(s.last(t).Body); !strings.Contains(body, "ana@example.com") {
		t.Error("with contact details on, the words should go as written")
	}
}

// Data the task cannot do without, in a class the store keeps in: the run
// stops and says so, rather than sending a request that saw nothing.
func TestARequiredClassThePolicyKeepsInStopsTheRun(t *testing.T) {
	task := summaryTask(func(json.RawMessage) []Message {
		return []Message{{Role: "user", Text: "Classify this message.", Data: []Data{
			{Class: DataMessageBodies, Label: "message", Value: "Where is my order?", Required: true},
		}}}
	})
	app, m, s := newGateway(t, reply(http.StatusOK, answer(smallID, summaryAnswer, smallUsage)), task)
	ctx := context.Background()
	configure(t, app, ctx, nil)

	res, err := m.Run(ctx, "test.summarise", nil)
	re := runErr(t, err)
	if re.Outcome != OutcomePolicyBlocked || re.Transient || !strings.Contains(re.Reason, "message_bodies") {
		t.Fatalf("error = %+v, want policy_blocked naming the class", re)
	}
	if s.count() != 0 {
		t.Error("a request was sent without the data it needed")
	}
	if e := mustExecution(t, m, ctx, res.ExecutionID); e.Outcome != OutcomePolicyBlocked {
		t.Errorf("record = %s", e.Outcome)
	}
	configure(t, app, ctx, map[string]any{"share_message_bodies": true})
	if _, err := m.Run(ctx, "test.summarise", nil); err != nil || s.count() != 1 {
		t.Errorf("with message bodies allowed: %v, %d calls", err, s.count())
	}
}

// A message the policy emptied is not sent: a model asked about nothing
// answers something, and that answer would be recorded as the task's.
func TestAMessageThePolicyEmptiedIsNotSent(t *testing.T) {
	task := summaryTask(func(json.RawMessage) []Message {
		return []Message{{Role: "user", Data: []Data{
			{Class: DataContactDetails, Label: "customer", Value: map[string]string{"name": "Ana Lima"}},
		}}}
	})
	app, m, s := newGateway(t, reply(http.StatusOK, answer(smallID, summaryAnswer, smallUsage)), task)
	ctx := context.Background()
	configure(t, app, ctx, nil)

	_, err := m.Run(ctx, "test.summarise", nil)
	if re := runErr(t, err); re.Outcome != OutcomePolicyBlocked || !strings.Contains(re.Reason, "empty") {
		t.Fatalf("error = %+v, want policy_blocked saying the message was emptied", re)
	}
	if s.count() != 0 {
		t.Error("an empty message was sent")
	}
}
