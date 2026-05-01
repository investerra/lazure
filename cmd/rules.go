package cmd

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"
	"sigs.k8s.io/yaml"

	"github.com/investerra/lazure/internal/azureapi"
	"github.com/investerra/lazure/internal/errs"
)

type rulesReport struct {
	ContainerApp azureapi.ContainerAppFieldMapping `json:"container_app"`
}

// Rules implements `lazure rules`. It prints Lazure's Container App
// ownership mapping: which Azure fields are managed, preserved, ignored,
// normalized, or rejected before deploy.
func Rules(ctx context.Context, c *cli.Command) error {
	_, _ = ctx, c
	out, err := yaml.Marshal(rulesReport{
		ContainerApp: azureapi.ContainerAppFieldMappingRules(),
	})
	if err != nil {
		return errs.System(errs.Wrap(err, "rules: marshal yaml"))
	}
	fmt.Print(string(out))
	return nil
}
