package cmd

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"
	"sigs.k8s.io/yaml"

	"github.com/investerra/lazure/internal/azureapi"
	"github.com/investerra/lazure/internal/errs"
)

type configReport struct {
	ContainerApp azureapi.ContainerAppFieldMapping `json:"container_app"`
}

// Config implements `lazure config`. It prints Lazure's Container App
// ownership mapping: which Azure fields are managed, preserved, ignored,
// normalized, or rejected before deploy.
func Config(ctx context.Context, c *cli.Command) error {
	_, _ = ctx, c
	out, err := yaml.Marshal(configReport{
		ContainerApp: azureapi.ContainerAppFieldMappingRules(),
	})
	if err != nil {
		return errs.System(errs.Wrap(err, "config: marshal yaml"))
	}
	fmt.Print(string(out))
	return nil
}
