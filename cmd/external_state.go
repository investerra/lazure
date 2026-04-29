package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/investerra/lazure/internal/errs"
)

func unsupportedLiveStateError(command string, fields []string) error {
	return errs.Errorf("%s would overwrite Azure Container App settings that lazure does not manage:\n\n  - %s\n\nMove these settings into lazure support, remove them from Azure, or explicitly mark them as preserved external state.",
		command,
		strings.Join(fields, "\n  - "))
}

func printUnsupportedLiveState(command string, fields []string) {
	if len(fields) == 0 {
		return
	}
	_, _ = fmt.Fprintf(os.Stderr,
		"%s\n\n",
		unsupportedLiveStateError(command, fields).Error())
}
