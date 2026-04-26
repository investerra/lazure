package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/investerra/lazure/internal/errs"
)

// ScaleFlags are the flags for `lazure scale`.
func ScaleFlags() []cli.Flag {
	return []cli.Flag{
		&cli.IntFlag{Name: "replicas", Usage: "pin replicas to this exact count (sets both min and max)"},
		&cli.IntFlag{Name: "min", Usage: "minimum replicas; defaults to current value if --max is set without it"},
		&cli.IntFlag{Name: "max", Usage: "maximum replicas; defaults to current value if --min is set without it"},
		&cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "skip the confirmation prompt"},
	}
}

// Scale implements `lazure scale <env>` — quick replica bound
// adjustment without editing the manifest. PATCHes only
// template.scale.{min,max}Replicas; everything else stays put.
//
// Three input modes:
//   --replicas=N            pin to exactly N (sets min=max=N)
//   --min=A --max=B         set both
//   --min=A (or --max=B)    keep the other side at its current value
//
// Useful for:
//   - incident response (scale up before traffic spike)
//   - cost saving (pin to 1 overnight)
//   - debugging (pin to 1 to make logs / exec deterministic)
//
// Manifest stays the source of truth: next deploy will reset bounds
// to whatever scale.min/max declares, unless the manifest is updated
// to match the new values.
func Scale(ctx context.Context, c *cli.Command) error {
	yes := c.Bool("yes")
	replicas := int(c.Int("replicas"))
	minR := int(c.Int("min"))
	maxR := int(c.Int("max"))

	// Validate input modes — exactly one of (--replicas, --min/--max).
	hasReplicas := c.IsSet("replicas")
	hasMinMax := c.IsSet("min") || c.IsSet("max")
	if hasReplicas && hasMinMax {
		return errs.Usage(errs.New("scale: --replicas is mutually exclusive with --min/--max"))
	}
	if !hasReplicas && !hasMinMax {
		return errs.Usage(errs.New("scale: pass --replicas=N or --min/--max"))
	}

	t, err := loadAzureTarget(c, "scale")
	if err != nil {
		return err
	}
	slog.Debug("scale: start", "env", t.Env, "replicas", replicas, "min", minR, "max", maxR)

	// Need the current state for two reasons: (1) when only one of
	// min/max is provided, the other defaults to current; (2) if the
	// new bounds equal current, skip the PATCH entirely.
	app, err := t.CA.Get(ctx, t.Sub, t.RG, t.Name)
	if err != nil {
		return errs.System(errs.Wrap(err, "scale: get"))
	}
	curMin, curMax := 1, 1 // ACA defaults when scale block is absent
	if app.Properties.Template.Scale != nil {
		curMin = app.Properties.Template.Scale.MinReplicas
		curMax = app.Properties.Template.Scale.MaxReplicas
	}

	newMin, newMax := curMin, curMax
	if hasReplicas {
		newMin, newMax = replicas, replicas
	} else {
		if c.IsSet("min") {
			newMin = minR
		}
		if c.IsSet("max") {
			newMax = maxR
		}
	}
	if newMin < 0 {
		return errs.Usage(errs.Errorf("scale: --min cannot be negative (got %d)", newMin))
	}
	if newMax < newMin {
		return errs.Usage(errs.Errorf("scale: --max (%d) must be >= --min (%d)", newMax, newMin))
	}
	if newMin == curMin && newMax == curMax {
		fmt.Printf("scale: already %d-%d, nothing to do\n", curMin, curMax)
		return nil
	}

	if !yes {
		fmt.Printf("\nscale %s\n  env: %s\n  sub: %s\n  rg:  %s\n  from: %d-%d\n  to:   %d-%d\n",
			t.Name, t.Env, t.SubLabel(), t.RG, curMin, curMax, newMin, newMax)
		if !promptConfirm("proceed?") {
			return errs.Usage(errs.New("scale: aborted by user"))
		}
	}

	slog.Info("scaling", "app", t.Name, "env", t.Env, "from", fmt.Sprintf("%d-%d", curMin, curMax), "to", fmt.Sprintf("%d-%d", newMin, newMax))
	start := time.Now()
	sp := newWaitSpinner(time.Time{})
	sp.SetMessage("ARM operation in progress")
	sp.Start()
	_, err = t.CA.PatchScaleAndWait(ctx, t.Sub, t.RG, t.Name, newMin, newMax)
	sp.Stop()
	if err != nil {
		return errs.System(errs.Wrap(err, "scale: patch"))
	}
	slog.Info("scale complete",
		"app", t.Name, "env", t.Env,
		"min", newMin, "max", newMax,
		"duration", time.Since(start).Round(time.Second))
	return nil
}
