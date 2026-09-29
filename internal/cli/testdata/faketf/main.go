package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type fakeTFConfig struct {
	Log         string          `json:"log"`
	Tofu        bool            `json:"tofu"`
	Version     string          `json:"version"`
	InitExit    int             `json:"init_exit"`
	Backend     json.RawMessage `json:"backend,omitempty"`
	PlanExit    int             `json:"plan_exit"`
	PlanOutput  string          `json:"plan_output"`
	ShowJSON    string          `json:"show_json"`
	ShowText    string          `json:"show_text"`
	ShowExit    int             `json:"show_exit"`
	ApplyExit   int             `json:"apply_exit"`
	ApplyOutput string          `json:"apply_output"`
	FailOutput  string          `json:"fail_output"`
}

func main() {
	os.Exit(run(os.Getenv("STACKORDER_TEST_FAKE_TF"), os.Args[1:]))
}

func run(cfgPath string, args []string) int {
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake terraform:", err)
		return 97
	}
	var cfg fakeTFConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		fmt.Fprintln(os.Stderr, "fake terraform:", err)
		return 97
	}
	if cfg.Log != "" {
		var line strings.Builder
		for _, a := range args {
			line.WriteString(a + "\x1f")
		}
		f, err := os.OpenFile(cfg.Log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = f.WriteString(line.String() + "\n")
			_ = f.Close()
		}
	}
	if len(args) == 0 {
		return 1
	}
	fail := func(code int) int {
		fmt.Fprintf(os.Stderr, "\nError: %s\n", cfg.FailOutput)
		return code
	}
	lastFile := func() (string, bool) {
		f := args[len(args)-1]
		_, err := os.Stat(f)
		return f, err == nil
	}
	switch args[0] {
	case "version":
		if cfg.Tofu {
			fmt.Printf(`{"terraform_version":%q,"platform":"linux_amd64","provider_selections":{}}`+"\n", cfg.Version)
		} else {
			fmt.Printf(`{"terraform_version":%q,"platform":"linux_amd64","provider_selections":{},"terraform_outdated":false}`+"\n", cfg.Version)
		}
	case "init":
		fmt.Println("Terraform has been successfully initialized!")
		if cfg.InitExit != 0 {
			return fail(cfg.InitExit)
		}
		if len(cfg.Backend) > 0 {
			_ = os.MkdirAll(".terraform", 0o750)
			_ = os.WriteFile(filepath.Join(".terraform", "terraform.tfstate"), cfg.Backend, 0o600)
		}
	case "workspace":
		fmt.Printf("Switched to workspace %q.\n", args[len(args)-1])
	case "plan":
		fmt.Print(cfg.PlanOutput)
		if cfg.PlanExit == 1 {
			return fail(1)
		}
		for _, a := range args {
			if out, ok := strings.CutPrefix(a, "-out="); ok {
				_ = os.WriteFile(out, []byte("fake plan"), 0o600)
			}
		}
		return cfg.PlanExit
	case "show":
		f, ok := lastFile()
		if !ok {
			fmt.Fprintf(os.Stderr, "Error: Failed to read the given file %s as a state or plan file\n", f)
			return 1
		}
		if cfg.ShowExit != 0 {
			return fail(cfg.ShowExit)
		}
		src := cfg.ShowText
		if slices.Contains(args, "-json") {
			src = cfg.ShowJSON
		}
		out, err := os.ReadFile(src)
		if err != nil {
			fmt.Fprintln(os.Stderr, "fake terraform:", err)
			return 97
		}
		_, _ = os.Stdout.Write(out)
	case "apply":
		f, ok := lastFile()
		if !ok {
			fmt.Fprintf(os.Stderr, "Error: Failed to load %q as a plan file\n", f)
			return 1
		}
		fmt.Print(cfg.ApplyOutput)
		if cfg.ApplyExit != 0 {
			return fail(cfg.ApplyExit)
		}
		fmt.Println("Apply complete! Resources: 1 added, 0 changed, 0 destroyed.")
	case "output":
		fmt.Println("{}")
	}
	return 0
}
