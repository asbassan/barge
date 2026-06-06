package output

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/olekukonko/tablewriter"
	"github.com/schollz/progressbar/v3"
)

var (
	Success = color.New(color.FgGreen, color.Bold)
	Info    = color.New(color.FgCyan)
	Warn    = color.New(color.FgYellow)
	Error   = color.New(color.FgRed, color.Bold)
	Faint   = color.New(color.Faint)
	Bold    = color.New(color.Bold)
)

func Infof(format string, args ...any)  { Info.Fprintf(os.Stdout, format+"\n", args...) }
func Warnf(format string, args ...any)  { Warn.Fprintf(os.Stderr, "WARNING: "+format+"\n", args...) }
func Errorf(format string, args ...any) { Error.Fprintf(os.Stderr, "Error: "+format+"\n", args...) }
func Successf(format string, args ...any) {
	Success.Fprintf(os.Stdout, "✓ "+format+"\n", args...)
}

func PrintTable(headers []string, rows [][]string) {
	t := tablewriter.NewWriter(os.Stdout)
	t.SetHeader(headers)
	t.SetBorder(false)
	t.SetHeaderAlignment(tablewriter.ALIGN_LEFT)
	t.SetAlignment(tablewriter.ALIGN_LEFT)
	t.SetCenterSeparator("")
	t.SetColumnSeparator("   ")
	t.SetRowSeparator("")
	t.SetHeaderLine(false)
	t.SetTablePadding("  ")
	t.SetNoWhiteSpace(true)
	t.AppendBulk(rows)
	t.Render()
}

func NewPullBar(description string, size int64) *progressbar.ProgressBar {
	return progressbar.NewOptions64(size,
		progressbar.OptionSetDescription(description),
		progressbar.OptionSetWriter(os.Stderr),
		progressbar.OptionShowBytes(true),
		progressbar.OptionSetWidth(40),
		progressbar.OptionThrottle(65*time.Millisecond),
		progressbar.OptionShowCount(),
		progressbar.OptionOnCompletion(func() { fmt.Fprintln(os.Stderr) }),
		progressbar.OptionSpinnerType(14),
		progressbar.OptionFullWidth(),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "=",
			SaucerHead:    ">",
			SaucerPadding: " ",
			BarStart:      "[",
			BarEnd:        "]",
		}),
	)
}

func ShortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func TruncateImage(ref string) string {
	if len(ref) > 50 {
		return ref[:47] + "..."
	}
	return ref
}

// HumanDuration returns a human-readable "X ago" string for a past time.Time.
func HumanDuration(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d seconds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}

func FormatSize(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.2f GB", float64(bytes)/GB)
	case bytes >= MB:
		return fmt.Sprintf("%.2f MB", float64(bytes)/MB)
	case bytes >= KB:
		return fmt.Sprintf("%.2f KB", float64(bytes)/KB)
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

func PrintBanner() {
	Bold.Print(`
  ██████╗  █████╗ ██████╗  ██████╗ ███████╗
  ██╔══██╗██╔══██╗██╔══██╗██╔════╝ ██╔════╝
  ██████╔╝███████║██████╔╝██║  ███╗█████╗
  ██╔══██╗██╔══██║██╔══██╗██║   ██║██╔══╝
  ██████╔╝██║  ██║██║  ██║╚██████╔╝███████╗
  ╚═════╝ ╚═╝  ╚═╝╚═╝  ╚═╝ ╚═════╝ ╚══════╝
  Windows Container Runtime — beginner friendly
`)
}

func Confirm(prompt string) bool {
	fmt.Printf("%s [y/N]: ", prompt)
	var response string
	fmt.Scanln(&response)
	return strings.ToLower(strings.TrimSpace(response)) == "y"
}
