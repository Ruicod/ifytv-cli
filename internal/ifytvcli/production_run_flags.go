package ifytvcli

import (
	"errors"
	"flag"
	"strconv"
	"strings"
)

type productionRunOptions struct {
	runID     string
	episodeID string
	requestID string
	operation string
	cursor    string
	planHash  string
	quoteID   string
	currency  string
	budget    *int64
	steps     []string
	replay    bool
}

func isProductionRunCommand(command string) bool {
	switch command {
	case "episode plan", "episode status", "episode quote", "episode resume":
		return true
	}
	return false
}

func addProductionRunFlags(f *flag.FlagSet, o *options) {
	p := &o.production
	switch o.command {
	case "episode plan":
		f.StringVar(&o.spec, "spec", "", "")
		addRevisionFlag(f, o)
	case "episode status":
		f.StringVar(&p.episodeID, "episode-id", "", "")
		f.StringVar(&p.operation, "operation", "", "")
		f.StringVar(&p.cursor, "cursor", "", "")
	case "episode resume":
		addRevisionFlag(f, o)
		f.StringVar(&p.quoteID, "quote-id", "", "")
		f.StringVar(&p.currency, "currency", "", "")
		f.Func("budget-units", "", func(value string) error {
			units, err := strconv.ParseInt(value, 10, 64)
			if err != nil || units < 0 || (p.budget != nil && *p.budget != units) {
				return errors.New("invalid budget")
			}
			p.budget = &units
			return nil
		})
	}
	if o.command != "episode plan" {
		f.StringVar(&p.runID, "run-id", "", "")
	}
	if o.command != "episode quote" {
		f.StringVar(&p.requestID, "request-id", "", "")
	}
	if o.command == "episode plan" || o.command == "episode resume" {
		f.BoolVar(&p.replay, "replay", false, "")
	}
	if o.command == "episode quote" || o.command == "episode resume" {
		f.StringVar(&p.planHash, "plan-hash", "", "")
		f.Func("step-id", "", func(id string) error {
			if !nodePattern.MatchString(id) || len(p.steps) >= 512 {
				return errors.New("invalid step ID")
			}
			for _, step := range p.steps {
				if step == id {
					return errors.New("duplicate step ID")
				}
			}
			p.steps = append(p.steps, id)
			return nil
		})
	}
}

func addRevisionFlag(f *flag.FlagSet, o *options) {
	f.Func("revision", "", func(value string) error {
		revision, err := strconv.ParseUint(value, 10, 64)
		if err != nil || revision == 0 || (o.revision != 0 && o.revision != revision) {
			return errors.New("invalid revision")
		}
		o.revision = revision
		return nil
	})
}

func validEpisodeOperation(operation string) bool {
	switch operation {
	case "plan", "resume", "render", "publish":
		return true
	}
	return false
}

func validateProductionRunOptions(o *options) *failure {
	target, problem := projectTarget(o.project)
	if problem != nil {
		return problem
	}
	o.project = target
	p := &o.production
	for _, value := range []*string{&p.runID, &p.episodeID, &p.requestID, &p.quoteID} {
		if *value != "" && !validUUID(*value) {
			return fail("usage", "Use UUIDs for episode, run, quote and request IDs.")
		}
		*value = strings.ToLower(*value)
	}
	switch o.command {
	case "episode plan":
		if o.spec == "" || o.revision == 0 || p.requestID == "" {
			return fail("usage", "Episode plan requires --spec FILE, --revision N and --request-id UUID.")
		}
	case "episode status":
		if p.cursor != "" && !validProductionCursor(p.cursor) {
			return fail("usage", "Use the cursor exactly as returned by the site.")
		}
		switch {
		case p.runID != "":
			if p.episodeID != "" || p.requestID != "" || p.operation != "" {
				return fail("usage", "Use --run-id on its own or choose an episode list or request lookup.")
			}
		case p.requestID != "":
			if !validEpisodeOperation(p.operation) || p.episodeID != "" || p.cursor != "" {
				return fail("usage", "Request lookup requires only --request-id and --operation plan, resume, render or publish.")
			}
		case p.operation != "":
			return fail("usage", "Use --operation only with --request-id.")
		}
	case "episode quote", "episode resume":
		p.planHash = strings.ToLower(p.planHash)
		if p.runID == "" || !validHash(p.planHash) {
			return fail("usage", "Episode quote and resume require --run-id UUID and --plan-hash SHA256.")
		}
		if o.command == "episode resume" {
			if o.revision == 0 || p.requestID == "" {
				return fail("usage", "Episode resume requires --revision N and --request-id UUID.")
			}
			if (p.budget == nil) != (p.currency == "") || (p.budget != nil && (p.quoteID == "" || !validBudgetCurrency(p.currency))) {
				return fail("usage", "Supply --quote-id UUID, --budget-units N and --currency CODE together for a new budget.")
			}
		}
	}
	return nil
}

func validBudgetCurrency(value string) bool {
	return len(value) <= 32 && nodePattern.MatchString(value)
}
