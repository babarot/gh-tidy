package recording

import (
	"context"
	"strings"
	"time"

	"github.com/babarot/gh-tidy/internal/github"
	"github.com/babarot/gh-tidy/internal/model"
)

// GitHub is the made-up GitHub of a world: it answers from the
// repositories built, the default branch of each from its bare remote, a
// little slower than at once, so that the screen moves as it would.
type GitHub struct{ w *World }

// GitHub returns the world's made-up GitHub.
func (w *World) GitHub() *GitHub { return &GitHub{w} }

// latency is how long each answer takes.
const latency = 350 * time.Millisecond

func (g *GitHub) wait(ctx context.Context) error {
	select {
	case <-time.After(latency):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *GitHub) rate(cost int) github.Rate {
	return github.Rate{Resource: "graphql", Limit: 5000, Remaining: 4852, Cost: cost, Reset: time.Now().Add(42 * time.Minute), Date: time.Now()}
}

func (g *GitHub) Host() string { return "github.com" }

func (g *GitHub) Viewer(context.Context) (github.Viewer, github.Rate, error) {
	return github.Viewer{Login: Owner}, g.rate(1), nil
}

func (g *GitHub) find(slug string) *repo {
	for i := range g.w.repos {
		if strings.EqualFold(Owner+"/"+g.w.repos[i].name, slug) {
			return &g.w.repos[i]
		}
	}
	return nil
}

func (g *GitHub) List(ctx context.Context, owner string, _ github.OwnerKind, _ github.Pages, _ int) (github.Listing, error) {
	if err := g.wait(ctx); err != nil {
		return github.Listing{}, err
	}
	if !strings.EqualFold(owner, Owner) {
		return github.Listing{}, github.ErrNoOwner
	}
	l := github.Listing{Completeness: github.Complete, Pages: github.Pages{ETags: []string{"made-up"}, Ended: true, Completeness: github.Complete}}
	for _, r := range g.w.repos {
		slug := Owner + "/" + r.name
		l.Repos = append(l.Repos, github.ListedRepo{Key: model.NewRemoteKey("github.com", slug), Name: r.name, NameWithOwner: slug,
			Private: r.private, PushedAt: g.w.Built.Add(-r.pushed), DefaultBranch: "main", Stars: r.stars})
	}
	l.Rate = g.rate(1)
	l.Rate.Resource = "core"
	return l, nil
}

func (g *GitHub) Heavy(ctx context.Context, slugs []string, want github.Want) ([]github.HeavyRepo, github.Rate, error) {
	if err := g.wait(ctx); err != nil {
		return nil, github.Rate{}, err
	}
	var out []github.HeavyRepo
	for _, s := range slugs {
		k := model.NewRemoteKey("github.com", s)
		r := g.find(s)
		if r == nil {
			out = append(out, github.HeavyRepo{Asked: k, Key: k})
			continue
		}
		h := github.HeavyRepo{Asked: k, Key: k, Found: true, PushedAt: g.w.Built.Add(-r.pushed), Default: "main", DefaultOid: r.head,
			CI: r.ci, Issues: len(r.issues), PRs: len(r.prs), Alerts: len(r.alerts), Stars: r.stars, ID: r.id}
		if want.Newest {
			for _, o := range r.issues {
				h.IssuesNewest = append(h.IssuesNewest, g.w.Built.Add(-o.made))
			}
			for _, o := range r.prs {
				h.PRsNewest = append(h.PRsNewest, g.w.Built.Add(-o.made))
			}
		}
		out = append(out, h)
	}
	return out, g.rate(1), nil
}

func (g *GitHub) RepoDetail(ctx context.Context, slug string, want github.DetailWant) (model.RepoDetail, github.Rate, error) {
	if err := g.wait(ctx); err != nil {
		return model.RepoDetail{}, github.Rate{}, err
	}
	r := g.find(slug)
	if r == nil {
		return model.RepoDetail{}, g.rate(1), &github.StatusError{Status: 404, Message: "not found"}
	}
	d := model.RepoDetail{AlertsAsked: want.Alerts, ChecksAsked: want.Oid != "", TitlesAsked: want.Titles}
	if want.Alerts {
		for _, a := range r.alerts {
			d.Alerts = append(d.Alerts, model.Alert{Package: a.pkg, Ecosystem: a.ecosystem, Severity: a.severity, Range: a.vulnerable, Fixed: a.fixed, Manifest: a.manifest})
		}
		d.AlertsTotal = len(d.Alerts)
		model.SortAlerts(d.Alerts)
	}
	if want.Oid != "" {
		switch r.ci {
		case "FAILURE":
			d.Checks, d.ChecksFailed = []model.Check{{Name: "test (ubuntu-latest)"}, {Name: "lint"}}, 2
		case "PENDING":
			d.Checks, d.ChecksRunning = []model.Check{{Name: "build", Running: true}}, 1
		}
	}
	if want.Titles {
		d.IssuesRead, d.PRsRead = true, true
		for _, o := range r.issues {
			d.Issues = append(d.Issues, model.Opened{Number: o.number, Title: o.title, Author: o.author, Created: g.w.Built.Add(-o.made)})
		}
		for _, o := range r.prs {
			d.PRs = append(d.PRs, model.Opened{Number: o.number, Title: o.title, Author: o.author, Created: g.w.Built.Add(-o.made)})
		}
	}
	return d, g.rate(1), nil
}
