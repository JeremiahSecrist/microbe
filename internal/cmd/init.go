package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// starterMicrobeNix is the microbe.nix an empty project starts from: one
// service, enough to `up` immediately and see a real stack come up before
// growing it. flake.nix/modules/*.nix are not scaffolded here — they're
// derived from this file by `up`/`build` (see internal/nix/flakegen) and
// written alongside it on the next run.
const starterMicrobeNix = `# microbe stack definition. Run 'microbe up' to build and start it;
# flake.nix and modules/*.nix will appear next to this file, rendered from
# what's declared here.
{
  name = "myapp";

  networks = {
    backend = { subnet = "192.168.60.0/24"; };
  };

  services = {
    web = {
      vcpu = 1;
      mem  = 512;

      config = { pkgs, ... }: {
        services.httpd.enable = true;
      };

      networks = [
        { name = "backend"; ip = "192.168.60.2"; }
      ];

      ports = [ "8080:80" ];
    };
  };
}
`

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Scaffold a starter microbe.nix in the current directory",
		Long: `Init writes a starter microbe.nix (-f/--file to pick the path) defining
one service, ready to run "microbe up" immediately. It refuses to overwrite
an existing file. flake.nix and modules/*.nix are not created here -- up and
build derive and write them next to microbe.nix from what it declares.`,
		Example: `  microbe init
  microbe -f stacks/myapp.nix init`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return initRun(file, os.Stdout)
		},
	}
}

func initRun(configFile string, out io.Writer) error {
	if _, err := os.Stat(configFile); err == nil {
		return fmt.Errorf("init: %s already exists", configFile)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(configFile), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(configFile, []byte(starterMicrobeNix), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(out, "wrote %s\n", configFile)
	fmt.Fprintln(out, "edit it, then run 'microbe up'")
	return nil
}
