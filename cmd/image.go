package cmd

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/investerra/lazure/internal/azurearm"
	"github.com/investerra/lazure/internal/errs"
)

type commandOutputRunner func(context.Context, string, ...string) ([]byte, error)

type imageRef struct {
	Full       string
	Server     string
	Repository string
	Tag        string
	Digest     string
}

func (r imageRef) ImageArg() string {
	if r.Digest != "" {
		return r.Repository + "@" + r.Digest
	}
	return r.Repository + ":" + r.Tag
}

func parseImageRef(image string) (imageRef, bool) {
	server, rest, ok := strings.Cut(image, "/")
	if !ok || server == "" || rest == "" {
		return imageRef{}, false
	}
	ref := imageRef{Full: image, Server: server}
	if repo, digest, ok := strings.Cut(rest, "@"); ok {
		ref.Repository = repo
		ref.Digest = digest
		return ref, ref.Repository != "" && ref.Digest != ""
	}
	lastSlash := strings.LastIndex(rest, "/")
	lastColon := strings.LastIndex(rest, ":")
	if lastColon > lastSlash {
		ref.Repository = rest[:lastColon]
		ref.Tag = rest[lastColon+1:]
	} else {
		ref.Repository = rest
		ref.Tag = "latest"
	}
	return ref, ref.Repository != "" && ref.Tag != ""
}

func checkDeployImages(ctx context.Context, app *azurearm.ContainerApp, runner commandOutputRunner) error {
	if app == nil {
		return nil
	}
	if runner == nil {
		if _, err := exec.LookPath("az"); err != nil {
			return errs.System(errs.New("deploy: 'az' not found on PATH (required to verify ACR image tags before deploy)"))
		}
		runner = runCombined
	}
	refs := uniqueACRImageRefs(app)
	for _, ref := range refs {
		acrName, ok := acrNameFromServer(ref.Server)
		if !ok {
			continue
		}
		args := []string{
			"acr", "repository", "show",
			"--name", acrName,
			"--image", ref.ImageArg(),
			"--only-show-errors",
			"--output", "none",
		}
		out, err := runner(ctx, "az", args...)
		if err != nil {
			detail := strings.TrimSpace(string(out))
			if detail != "" {
				return errs.Validation(errs.Errorf("deploy: image %s is missing or not accessible in ACR %s: %s", ref.Full, acrName, detail))
			}
			return errs.Validation(errs.Wrapf(err, "deploy: image %s is missing or not accessible in ACR %s", ref.Full, acrName))
		}
	}
	return nil
}

func uniqueACRImageRefs(app *azurearm.ContainerApp) []imageRef {
	seen := map[string]imageRef{}
	add := func(image string) {
		ref, ok := parseImageRef(image)
		if !ok {
			return
		}
		if _, ok := acrNameFromServer(ref.Server); !ok {
			return
		}
		seen[ref.Full] = ref
	}
	for _, c := range app.Properties.Template.Containers {
		add(c.Image)
	}
	for _, c := range app.Properties.Template.InitContainers {
		add(c.Image)
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]imageRef, 0, len(keys))
	for _, k := range keys {
		out = append(out, seen[k])
	}
	return out
}

func applyRuntimeEnvOverrides(app *azurearm.ContainerApp, values map[string]string) {
	if app == nil || len(values) == 0 {
		return
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i := range app.Properties.Template.Containers {
		for _, k := range keys {
			app.Properties.Template.Containers[i].Env = upsertPlainEnv(
				app.Properties.Template.Containers[i].Env,
				k,
				values[k],
			)
		}
	}
}

func parseDeployEnvOverrides(raw []string) (map[string]string, error) {
	values, err := parseKeyValueFlags(raw, "--env")
	if err != nil {
		return nil, err
	}
	for k := range values {
		if strings.TrimSpace(k) != k {
			return nil, errs.Usage(errs.Errorf("invalid --env %q (env var name has surrounding whitespace)", fmt.Sprintf("%s=%s", k, values[k])))
		}
	}
	return values, nil
}
