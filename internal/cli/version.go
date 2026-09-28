package cli

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/stackorder/stackorder/internal/version"
)

type versionInfo struct {
	Version  string `json:"version"`
	Commit   string `json:"commit"`
	Date     string `json:"date"`
	Go       string `json:"go"`
	Platform string `json:"platform"`
}

func (a *app) versionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the stackorder version",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if err := a.requireFormat(false); err != nil {
				return err
			}
			if a.format == formatJSON {
				return a.writeJSON(versionInfo{
					Version:  version.Version,
					Commit:   version.Commit,
					Date:     version.Date,
					Go:       runtime.Version(),
					Platform: runtime.GOOS + "/" + runtime.GOARCH,
				})
			}
			_, err := fmt.Fprintln(a.stdout, "stackorder", version.String())
			return err
		},
	}
}
