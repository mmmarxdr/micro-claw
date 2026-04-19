package notify

import (
	"context"
	"log/slog"
	"sync"
	"text/template"
	"time"

	"daimon/internal/config"
)

// compiledRule pairs a NotificationRule with its pre-compiled message template.
type compiledRule struct {
	config.NotificationRule
	tmpl *template.Template // nil if Template was empty or failed to parse
}

// RulesEngine evaluates incoming events against notification rules and dispatches
// matched rules to the NotificationSender. It is safe for concurrent use.
type RulesEngine struct {
	rules     []compiledRule
	sender    *NotificationSender
	mu        sync.Mutex
	lastFired map[string]time.Time // key: rule.Name → last fire timestamp
	sem       chan struct{}         // semaphore: limits concurrent notification sends
}

// NewRulesEngine creates a RulesEngine with pre-compiled templates.
// Returns an error if any rule has a template that fails to parse.
func NewRulesEngine(rules []config.NotificationRule, sender *NotificationSender) (*RulesEngine, error) {
	compiled := make([]compiledRule, 0, len(rules))
	for _, r := range rules {
		cr := compiledRule{NotificationRule: r}
		if r.Template != "" {
			tmpl, err := template.New(r.Name).Parse(r.Template)
			if err != nil {
				return nil, &TemplateParseError{Rule: r.Name, Err: err}
			}
			cr.tmpl = tmpl
		}
		compiled = append(compiled, cr)
	}

	return &RulesEngine{
		rules:     compiled,
		sender:    sender,
		lastFired: make(map[string]time.Time, len(rules)),
		sem:       make(chan struct{}, 10),
	}, nil
}

// Handle evaluates all rules against the event. It is registered as a Bus subscriber
// and called from the bus worker goroutine. Must not block — dispatch goroutines.
func (r *RulesEngine) Handle(event Event) {
	for _, rule := range r.rules {
		// 1. Event type must match.
		if event.Type != rule.EventType {
			continue
		}

		// 2. Job ID filter (optional).
		if rule.JobID != "" && event.JobID != rule.JobID {
			continue
		}

		// 3. Cooldown check.
		cooldown := time.Duration(rule.CooldownSec) * time.Second
		r.mu.Lock()
		if cooldown > 0 {
			if last, ok := r.lastFired[rule.Name]; ok {
				if time.Since(last) < cooldown {
					r.mu.Unlock()
					slog.Debug("notify: rule suppressed by cooldown",
						"rule", rule.Name,
						"cooldown_sec", rule.CooldownSec,
					)
					continue
				}
			}
		}
		r.lastFired[rule.Name] = time.Now()
		r.mu.Unlock()

		// 4. Dispatch fire-and-forget (bounded by semaphore to limit concurrency).
		ruleSnap := rule.NotificationRule // capture loop variable
		r.sem <- struct{}{}
		go func() {
			defer func() { <-r.sem }()
			if err := r.sender.Send(context.Background(), ruleSnap, event); err != nil {
				slog.Warn("notify: rule send failed",
					"rule", ruleSnap.Name,
					"event", event.Type,
					"error", err,
				)
			}
		}()
	}
}

// TemplateParseError is returned by NewRulesEngine when a rule template fails to parse.
type TemplateParseError struct {
	Rule string
	Err  error
}

func (e *TemplateParseError) Error() string {
	return "notify: rule " + e.Rule + " has invalid template: " + e.Err.Error()
}

func (e *TemplateParseError) Unwrap() error { return e.Err }
