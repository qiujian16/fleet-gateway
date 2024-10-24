package main

import (
	"fmt"
	"os"

	"github.com/spf13/pflag"

	"github.com/qiujian16/fleet-gateway/cli/kubectl-gw/cmd"
)

func main() {
	flags := pflag.NewFlagSet("kubectl-gw", pflag.ExitOnError)
	pflag.CommandLine = flags

	cmd := cmd.KubectlGWCommand()
	if err := cmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
