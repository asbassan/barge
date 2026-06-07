package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/asbassan/barge/internal/build"
	"github.com/asbassan/barge/internal/client"
	"github.com/asbassan/barge/internal/output"
	"github.com/asbassan/barge/internal/preflight"
	"github.com/asbassan/barge/internal/setup"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

const version = "0.1.0"

func main() {
	root := buildRoot()
	if err := root.Execute(); err != nil {
		if err.Error() != "" {
			output.Errorf("%v", err)
		}
		os.Exit(1)
	}
}

func buildRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "barge",
		Short: "BARGE — Windows Container Runtime",
		Long: `BARGE is a beginner-friendly Windows container tool.

Run Windows applications in isolated Hyper-V containers — no deep knowledge
of Windows internals required. Images are compatible with Docker Hub and the
Microsoft Container Registry (mcr.microsoft.com).

Examples:
  barge pull mcr.microsoft.com/windows/nanoserver:ltsc2022
  barge run mcr.microsoft.com/windows/nanoserver:ltsc2022 -- ipconfig
  barge ps
  barge stop <container-id>`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			// Skip checks for commands that don't touch the daemon.
			skip := map[string]bool{"version": true, "help": true, "completion": true, "init": true}
			if skip[cmd.Name()] {
				return nil
			}
			if err := preflight.Check(); err != nil {
				output.Errorf("%v", err)
				return fmt.Errorf("") // suppress duplicate print
			}
			return nil
		},
	}

	root.AddCommand(
		newVersionCmd(),
		newInitCmd(),
		newPullCmd(),
		newImagesCmd(),
		newRmiCmd(),
		newRunCmd(),
		newPsCmd(),
		newStopCmd(),
		newRmCmd(),
		newLogsCmd(),
		newExecCmd(),
		newInspectCmd(),
		newCommitCmd(),
		newBuildCmd(),
		newLoginCmd(),
		newLogoutCmd(),
		newTagCmd(),
		newPushCmd(),
		newStatsCmd(),
	)
	return root
}

// ── version ──────────────────────────────────────────────────────────────────

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show BARGE and containerd versions",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Printf("BARGE version %s\n", version)

			cl, err := client.New()
			if err != nil {
				output.Warnf("containerd not reachable: %v", err)
				return nil
			}
			defer cl.Close()

			ctrdVer, err := cl.Version(cmd.Context())
			if err != nil {
				output.Warnf("cannot get containerd version: %v", err)
				return nil
			}
			fmt.Printf("containerd version %s\n", ctrdVer)
			return nil
		},
	}
}

// ── init ─────────────────────────────────────────────────────────────────────

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Install BARGE prerequisites (Hyper-V, containerd)",
		Long: `Set up everything BARGE needs to run Windows containers.

Run this once on a new machine. It will:
  1. Enable Hyper-V
  2. Enable the Windows Containers feature
  3. Download and install containerd
  4. Start the containerd service

Requires administrator privileges. A reboot may be needed after enabling
Windows features — barge init will tell you if so, and you can re-run it
after rebooting to complete the remaining steps.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := preflight.CheckAdmin(); err != nil {
				return err
			}
			return setup.RunInit()
		},
	}
}

// ── pull ─────────────────────────────────────────────────────────────────────

func newPullCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pull <image>",
		Short: "Download a Windows container image",
		Long: `Download a Windows container image from a registry.

Examples:
  barge pull mcr.microsoft.com/windows/nanoserver:ltsc2022
  barge pull mcr.microsoft.com/windows/servercore:ltsc2022
  barge pull myregistry.azurecr.io/myapp:latest`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := client.New()
			if err != nil {
				return err
			}
			defer cl.Close()

			img, err := cl.Pull(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			output.Successf("Pulled %s", img.Name())
			return nil
		},
	}
}

// ── images ────────────────────────────────────────────────────────────────────

func newImagesCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "images",
		Short:   "List local images",
		Aliases: []string{"image", "img"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			cl, err := client.New()
			if err != nil {
				return err
			}
			defer cl.Close()

			images, err := cl.ListImages(cmd.Context())
			if err != nil {
				return err
			}
			if len(images) == 0 {
				fmt.Println("No images found. Pull one with: barge pull <image>")
				return nil
			}

			rows := make([][]string, len(images))
			for i, img := range images {
				rows[i] = []string{
					img.Name,
					output.ShortID(img.Digest),
					output.FormatSize(img.Size),
					output.HumanDuration(img.CreatedAt),
				}
			}
			output.PrintTable([]string{"IMAGE", "DIGEST", "SIZE", "CREATED"}, rows)
			return nil
		},
	}
}

// ── rmi ───────────────────────────────────────────────────────────────────────

func newRmiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rmi <image> [image...]",
		Short: "Remove one or more local images",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := client.New()
			if err != nil {
				return err
			}
			defer cl.Close()

			for _, ref := range args {
				if err := cl.RemoveImage(cmd.Context(), ref); err != nil {
					output.Errorf("%v", err)
					continue
				}
				output.Successf("Removed %s", ref)
			}
			return nil
		},
	}
}

// ── run ───────────────────────────────────────────────────────────────────────

func newRunCmd() *cobra.Command {
	var (
		opts      client.RunOptions
		isolation string
		envFile   string
	)

	cmd := &cobra.Command{
		Use:   "run [flags] <image> [-- command args...]",
		Short: "Create and start a Hyper-V isolated container",
		Long: `Create and start a Windows container using Hyper-V isolation.

Hyper-V isolation gives each container its own Windows kernel, so a
compromised container cannot affect the host.

Examples:
  barge run mcr.microsoft.com/windows/nanoserver:ltsc2022
  barge run mcr.microsoft.com/windows/nanoserver:ltsc2022 -- ipconfig
  barge run -d --name webserver myimage:latest
  barge run --env-file .env myapp:latest
  barge run --rm mcr.microsoft.com/windows/nanoserver:ltsc2022 -- hostname`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Image = args[0]
			opts.Isolation = client.Isolation(isolation)

			if cmd.ArgsLenAtDash() >= 0 {
				opts.Args = args[cmd.ArgsLenAtDash():]
			}

			if envFile != "" {
				fileEnv, err := readEnvFile(envFile)
				if err != nil {
					return err
				}
				opts.Env = append(opts.Env, fileEnv...)
			}

			cl, err := client.New()
			if err != nil {
				return err
			}
			defer cl.Close()

			id, err := cl.Run(cmd.Context(), opts)
			if err != nil {
				return err
			}

			if opts.Detach {
				fmt.Println(id)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&opts.Name, "name", "", "Assign a name to the container")
	cmd.Flags().BoolVarP(&opts.Detach, "detach", "d", false, "Run container in background")
	cmd.Flags().BoolVar(&opts.Remove, "rm", false, "Automatically remove container when it exits")
	cmd.Flags().StringArrayVarP(&opts.Env, "env", "e", nil, "Set environment variables (KEY=VALUE)")
	cmd.Flags().StringVar(&envFile, "env-file", "", "Read environment variables from a file (KEY=VALUE lines)")
	cmd.Flags().StringArrayVarP(&opts.Volumes, "volume", "v", nil, "Bind mount a volume (host:container[:ro])")
	cmd.Flags().StringArrayVarP(&opts.Ports, "publish", "p", nil, "Publish a port (host:container[/proto])")
	cmd.Flags().StringVar(&isolation, "isolation", "hyperv", "Isolation mode: hyperv (default) or process")
	return cmd
}

// readEnvFile parses KEY=VALUE lines from path, skipping blanks and # comments.
func readEnvFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open env file %q: %w", path, err)
	}
	defer f.Close()

	var result []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(line, "=") {
			return nil, fmt.Errorf("env file %q: invalid line %q (expected KEY=VALUE)", path, line)
		}
		result = append(result, line)
	}
	return result, scanner.Err()
}

// ── ps ────────────────────────────────────────────────────────────────────────

func newPsCmd() *cobra.Command {
	var showAll bool

	cmd := &cobra.Command{
		Use:   "ps",
		Short: "List containers",
		Long: `List containers managed by BARGE.

By default only running containers are shown. Use -a to see all.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cl, err := client.New()
			if err != nil {
				return err
			}
			defer cl.Close()

			containers, err := cl.ListContainers(cmd.Context(), showAll)
			if err != nil {
				return err
			}
			if len(containers) == 0 {
				if showAll {
					fmt.Println("No containers found. Start one with: barge run <image>")
				} else {
					fmt.Println("No running containers. Use 'barge ps -a' to see all.")
				}
				return nil
			}

			rows := make([][]string, len(containers))
			for i, c := range containers {
				rows[i] = []string{
					output.ShortID(c.ID),
					output.TruncateImage(c.Image),
					string(c.Status),
					output.HumanDuration(c.CreatedAt),
					c.Name,
				}
			}
			output.PrintTable([]string{"CONTAINER ID", "IMAGE", "STATUS", "CREATED", "NAMES"}, rows)
			return nil
		},
	}

	cmd.Flags().BoolVarP(&showAll, "all", "a", false, "Show all containers (including stopped)")
	return cmd
}

// ── stop ─────────────────────────────────────────────────────────────────────

func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop <container> [container...]",
		Short: "Stop one or more running containers",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := client.New()
			if err != nil {
				return err
			}
			defer cl.Close()

			for _, id := range args {
				if err := cl.StopContainer(cmd.Context(), id); err != nil {
					output.Errorf("%v", err)
					continue
				}
				fmt.Println(id)
			}
			return nil
		},
	}
}

// ── rm ────────────────────────────────────────────────────────────────────────

func newRmCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "rm <container> [container...]",
		Short: "Remove one or more stopped containers",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := client.New()
			if err != nil {
				return err
			}
			defer cl.Close()

			for _, id := range args {
				if err := cl.RemoveContainer(cmd.Context(), id, force); err != nil {
					output.Errorf("%v", err)
					continue
				}
				fmt.Println(id)
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Force remove a running container")
	return cmd
}

// ── logs ─────────────────────────────────────────────────────────────────────

func newLogsCmd() *cobra.Command {
	var follow bool

	cmd := &cobra.Command{
		Use:   "logs <container>",
		Short: "Fetch the logs of a detached container",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := client.New()
			if err != nil {
				return err
			}
			defer cl.Close()

			return cl.Logs(cmd.Context(), args[0], follow)
		},
	}

	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output")
	return cmd
}

// ── exec ─────────────────────────────────────────────────────────────────────

func newExecCmd() *cobra.Command {
	var interactive bool

	cmd := &cobra.Command{
		Use:   "exec [flags] <container> <command> [args...]",
		Short: "Run a command in a running container",
		Long: `Run a command inside a running BARGE container.

Examples:
  barge exec mycontainer ipconfig
  barge exec -it mycontainer cmd.exe
  barge exec mycontainer powershell -Command Get-Process`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := client.New()
			if err != nil {
				return err
			}
			defer cl.Close()

			return cl.Exec(cmd.Context(), client.ExecOptions{
				ContainerID: args[0],
				Args:        args[1:],
				Interactive: interactive,
			})
		},
	}

	cmd.Flags().BoolVarP(&interactive, "interactive", "i", false, "Keep stdin open")
	cmd.Flags().SetInterspersed(false) // stop flag parsing after container name so -Flag args pass through
	return cmd
}

// ── inspect ───────────────────────────────────────────────────────────────────

func newInspectCmd() *cobra.Command {
	var isImage bool

	cmd := &cobra.Command{
		Use:   "inspect <id>",
		Short: "Show detailed information about a container or image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := client.New()
			if err != nil {
				return err
			}
			defer cl.Close()

			var result string
			if isImage {
				result, err = cl.InspectImage(cmd.Context(), args[0])
			} else {
				result, err = cl.InspectContainer(cmd.Context(), args[0])
				if err != nil && strings.Contains(err.Error(), "not found") {
					// Try image if container not found.
					result, err = cl.InspectImage(cmd.Context(), args[0])
				}
			}
			if err != nil {
				return err
			}
			fmt.Println(result)
			return nil
		},
	}

	cmd.Flags().BoolVar(&isImage, "image", false, "Inspect an image instead of a container")
	return cmd
}

// ── commit ────────────────────────────────────────────────────────────────────

func newCommitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "commit <container> <image>",
		Short: "Create a new image from a container's current state",
		Long: `Save a stopped container's filesystem as a new local image.

Examples:
  barge commit mycontainer myapp:v1
  barge commit brave_dock myapp:latest`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := client.New()
			if err != nil {
				return err
			}
			defer cl.Close()

			if err := cl.CommitContainer(cmd.Context(), args[0], args[1], client.CommitOptions{}); err != nil {
				return err
			}
			output.Successf("Committed %s → %s", args[0], args[1])
			return nil
		},
	}
}

// ── build ─────────────────────────────────────────────────────────────────────

func newBuildCmd() *cobra.Command {
	var (
		tag           string
		bargefilePath string
		buildArgs     []string
	)

	cmd := &cobra.Command{
		Use:   "build [flags] <context-dir>",
		Short: "Build an image from a Bargefile",
		Long: `Build a Windows container image by executing instructions in a Bargefile.

The context-dir is the directory containing the Bargefile and any files
referenced by COPY instructions.

Supported instructions:
  FROM    <image>               Base image to build from
  COPY    <src> <dest>          Copy files from the build context into the container
  RUN     <command>             Execute a command inside the container
  ENV     KEY=VALUE             Set an environment variable in the image
  CMD     ["cmd", "args..."]    Default command when the container starts
  WORKDIR <path>                Set the working directory for subsequent RUN instructions
  EXPOSE  <port> [port...]      Document ports the container listens on
  ARG     NAME[=default]        Build-time variable

Examples:
  barge build -t myapp:v1 .
  barge build -t myapp:latest --file MyBargefile .
  barge build --build-arg VERSION=3.11 -t myapp:v1 .`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			contextDir := args[0]

			f, err := os.Open(bargefilePath)
			if err != nil {
				return fmt.Errorf("cannot open %s: %w", bargefilePath, err)
			}
			defer f.Close()

			bf, err := build.Parse(f)
			if err != nil {
				return fmt.Errorf("parse error: %w", err)
			}

			if tag == "" {
				return fmt.Errorf("--tag / -t is required")
			}

			cl, err := client.New()
			if err != nil {
				return err
			}
			defer cl.Close()

			return build.NewBuilder(cl).Build(cmd.Context(), bf, contextDir, tag, buildArgs)
		},
	}

	cmd.Flags().StringVarP(&tag, "tag", "t", "", "Name and optionally tag for the output image (required)")
	cmd.Flags().StringVar(&bargefilePath, "file", "Bargefile", "Path to the Bargefile")
	cmd.Flags().StringArrayVar(&buildArgs, "build-arg", nil, "Set build-time variables (KEY=VALUE)")
	return cmd
}

// ── login ─────────────────────────────────────────────────────────────────────

func newLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login <registry>",
		Short: "Log in to a container registry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			registry := args[0]

			fmt.Print("Username: ")
			var username string
			if _, err := fmt.Scan(&username); err != nil {
				return fmt.Errorf("cannot read username: %w", err)
			}

			fmt.Print("Password: ")
			passwordBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Println() // newline after silent input
			if err != nil {
				return fmt.Errorf("cannot read password: %w", err)
			}

			cl, err := client.New()
			if err != nil {
				return err
			}
			defer cl.Close()

			if err := cl.Login(cmd.Context(), registry, username, string(passwordBytes)); err != nil {
				return err
			}
			output.Successf("Logged in to %s", registry)
			return nil
		},
	}
}

// ── logout ────────────────────────────────────────────────────────────────────

func newLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout <registry>",
		Short: "Log out from a container registry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := client.New()
			if err != nil {
				return err
			}
			defer cl.Close()

			if err := cl.Logout(cmd.Context(), args[0]); err != nil {
				return err
			}
			output.Successf("Logged out from %s", args[0])
			return nil
		},
	}
}

// ── tag ───────────────────────────────────────────────────────────────────────

func newTagCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tag <src-image> <dst-image>",
		Short: "Tag a local image with a new name",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := client.New()
			if err != nil {
				return err
			}
			defer cl.Close()

			if err := cl.TagImage(cmd.Context(), args[0], args[1]); err != nil {
				return err
			}
			output.Successf("Tagged %s as %s", args[0], args[1])
			return nil
		},
	}
}

// ── push ──────────────────────────────────────────────────────────────────────

func newPushCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "push <image>",
		Short: "Push an image to a registry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := client.New()
			if err != nil {
				return err
			}
			defer cl.Close()

			if err := cl.PushImage(cmd.Context(), args[0]); err != nil {
				return err
			}
			output.Successf("Pushed %s", args[0])
			return nil
		},
	}
}

// ── stats ─────────────────────────────────────────────────────────────────────

func newStatsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stats <container>",
		Short: "Display resource usage statistics for a container",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := client.New()
			if err != nil {
				return err
			}
			defer cl.Close()

			return cl.Stats(cmd.Context(), args[0])
		},
	}
}
