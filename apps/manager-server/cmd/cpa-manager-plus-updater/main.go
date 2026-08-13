package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	updatecore "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/update"
)

func main() {
	if len(os.Args) < 2 {
		fail("no command was provided")
	}
	switch os.Args[1] {
	case "enable":
		runEnable(os.Args[2:])
	case "apply":
		runApply(os.Args[2:])
	case "recover":
		runRecover(os.Args[2:])
	default:
		fail("unsupported command: " + os.Args[1])
	}
}

func runRecover(arguments []string) {
	flags := flag.NewFlagSet("recover", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	manifest := flags.String("manifest", "", "managed install manifest path")
	if err := flags.Parse(arguments); err != nil {
		fail(err.Error())
	}
	status, recovered, err := updatecore.RecoverInterruptedUpdate(*manifest)
	if err != nil {
		fail(err.Error())
	}
	if recovered {
		_, _ = fmt.Fprintf(os.Stdout, "Recovered interrupted update %s (%s).\n", status.TransactionID, status.State)
	}
}

func runApply(arguments []string) {
	flags := flag.NewFlagSet("apply", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	transaction := flags.String("transaction", "", "update transaction path")
	if err := flags.Parse(arguments); err != nil {
		fail(err.Error())
	}
	if err := updatecore.ApplyTransaction(context.Background(), *transaction); err != nil {
		fail(err.Error())
	}
}

func runEnable(arguments []string) {
	flags := flag.NewFlagSet("enable", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	manifest := flags.String("manifest", "", "managed install manifest path")
	root := flags.String("install-root", "", "managed install root")
	binary := flags.String("binary", "", "Manager Server binary path")
	control := flags.String("control-script", "", "official control script path")
	updater := flags.String("updater", "", "updater binary path")
	backups := flags.String("backup-root", "", "managed backup directory")
	if err := flags.Parse(arguments); err != nil {
		fail(err.Error())
	}
	result, err := updatecore.EnableManagedUpdates(updatecore.EnableOptions{
		ManifestPath:  *manifest,
		InstallRoot:   *root,
		BinaryPath:    *binary,
		ControlScript: *control,
		UpdaterPath:   *updater,
		BackupRoot:    *backups,
	})
	if err != nil {
		fail(err.Error())
	}
	_, _ = fmt.Fprintf(os.Stdout, "Managed native updates enabled for install %s.\n", result.InstallID)
}

func fail(message string) {
	_, _ = fmt.Fprintln(os.Stderr, "cpa-manager-plus-updater: "+message)
	os.Exit(2)
}
