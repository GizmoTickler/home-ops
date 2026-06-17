// Package ui provides the interactive layer of homeops-cli: prompts, fuzzy
// selection, spinners, and styled output. It is built natively on
// charmbracelet huh/bubbletea/lipgloss — no external binary (gum) is needed.
// Every primitive degrades to plain line-mode I/O when stdin/stdout is not a
// terminal or HOMEOPS_NO_INTERACTIVE=1 is set, so scripts and CI behave.
package ui

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattn/go-isatty"

	"homeops-cli/internal/common"
	"homeops-cli/internal/constants"
)

// isInteractiveDisabled checks if interactive mode is explicitly disabled via
// environment variable.
func isInteractiveDisabled() bool {
	return os.Getenv(constants.EnvHomeOpsNoInteract) == "1"
}

// isInteractive reports whether rich TUI prompts can run: a real terminal on
// both ends and interactivity not explicitly disabled.
func isInteractive() bool {
	if isInteractiveDisabled() {
		return false
	}
	return isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd())
}

// assumeYes, when enabled via the global --yes flag, makes Confirm answer every
// confirmation prompt affirmatively without blocking on user input.
var assumeYes = false

// SetAssumeYes toggles automatic confirmation for all Confirm prompts.
// Wired to the root command's --yes/-y flag.
func SetAssumeYes(v bool) {
	assumeYes = v
}

// IsCancellation checks if an error is from user cancellation (Ctrl+C)
func IsCancellation(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, huh.ErrUserAborted) ||
		strings.Contains(err.Error(), "cancelled by user") ||
		strings.Contains(err.Error(), "cancelled")
}

var errCancelled = fmt.Errorf("cancelled by user")

// runField runs a single huh field as a form with the shared theme, mapping
// user aborts to the package's cancellation error.
func runField(field huh.Field) error {
	form := huh.NewForm(huh.NewGroup(field)).WithTheme(huh.ThemeFunc(huh.ThemeCharm))
	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return errCancelled
		}
		return err
	}
	return nil
}

// StyleOptions defines styling options for text
type StyleOptions struct {
	Foreground string
	Background string
	Bold       bool
	Italic     bool
	Border     string
}

// Confirm presents a yes/no confirmation prompt. Falls back to basic
// fmt.Scanln input when not running on an interactive terminal.
func Confirm(message string, defaultYes bool) (bool, error) {
	if assumeYes {
		// Leave an audit trail of what was auto-confirmed.
		fmt.Fprintf(os.Stderr, "%s yes (--yes)\n", message)
		return true, nil
	}
	if !isInteractive() {
		return confirmBasic(message, defaultYes)
	}

	confirmed := defaultYes
	field := huh.NewConfirm().Title(message).Affirmative("Yes").Negative("No").Value(&confirmed)
	if err := runField(field); err != nil {
		return false, err
	}
	return confirmed, nil
}

// confirmBasic is a fallback confirmation prompt using basic input
func confirmBasic(message string, defaultYes bool) (bool, error) {
	prompt := message
	if defaultYes {
		prompt += " (Y/n): "
	} else {
		prompt += " (y/N): "
	}

	fmt.Print(prompt)
	var response string
	_, err := fmt.Scanln(&response)
	if err != nil && err.Error() != "unexpected newline" {
		return false, err
	}

	response = strings.ToLower(strings.TrimSpace(response))

	// Empty response uses default
	if response == "" {
		return defaultYes, nil
	}

	return response == "y" || response == "yes", nil
}

// Choose presents a list of options for the user to select one.
// Returns the selected option as a string.
func Choose(prompt string, options []string) (string, error) {
	if !isInteractive() {
		return chooseBasic(prompt, options)
	}

	var selected string
	field := huh.NewSelect[string]().Title(prompt).Options(huh.NewOptions(options...)...).Value(&selected)
	if err := runField(field); err != nil {
		return "", err
	}
	return selected, nil
}

// chooseBasic is a fallback chooser using numbered list
func chooseBasic(prompt string, options []string) (string, error) {
	fmt.Println(prompt)
	for i, opt := range options {
		fmt.Printf("%d) %s\n", i+1, opt)
	}
	fmt.Print("Enter number: ")

	var choice int
	_, err := fmt.Scanln(&choice)
	if err != nil {
		return "", err
	}

	if choice < 1 || choice > len(options) {
		return "", fmt.Errorf("invalid choice")
	}

	return options[choice-1], nil
}

// ChooseMulti presents a list of options for the user to select multiple.
// Returns the selected options as a string slice.
func ChooseMulti(prompt string, options []string, limit int) ([]string, error) {
	if !isInteractive() {
		return chooseMultiBasic(prompt, options)
	}

	var selected []string
	field := huh.NewMultiSelect[string]().Title(prompt).Options(huh.NewOptions(options...)...).Value(&selected)
	if limit > 0 {
		field = field.Limit(limit)
	}
	if err := runField(field); err != nil {
		return nil, err
	}
	if selected == nil {
		selected = []string{}
	}
	return selected, nil
}

// chooseMultiBasic is a fallback multi-choice using comma-separated input
func chooseMultiBasic(prompt string, options []string) ([]string, error) {
	fmt.Println(prompt)
	for i, opt := range options {
		fmt.Printf("%d) %s\n", i+1, opt)
	}
	fmt.Print("Enter numbers (comma-separated): ")

	var input string
	_, err := fmt.Scanln(&input)
	if err != nil {
		return nil, err
	}

	choices := strings.Split(input, ",")
	result := []string{}
	for _, c := range choices {
		choice := strings.TrimSpace(c)
		var idx int
		_, err := fmt.Sscanf(choice, "%d", &idx)
		if err != nil || idx < 1 || idx > len(options) {
			continue
		}
		result = append(result, options[idx-1])
	}

	return result, nil
}

// Filter provides fuzzy filtering for a list of options (type to filter).
// Returns the selected option as a string.
func Filter(prompt string, options []string) (string, error) {
	if !isInteractive() {
		return chooseBasic(prompt, options)
	}

	var selected string
	field := huh.NewSelect[string]().Title(prompt).Options(huh.NewOptions(options...)...).Filtering(true).Value(&selected)
	if err := runField(field); err != nil {
		return "", err
	}
	return selected, nil
}

// Input prompts for a single-line text input.
// Returns the entered text as a string.
func Input(prompt, placeholder string) (string, error) {
	if !isInteractive() {
		return inputBasic(prompt)
	}

	var value string
	field := huh.NewInput().Title(prompt).Placeholder(placeholder).Value(&value)
	if err := runField(field); err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

// inputBasic is a fallback input using basic fmt
func inputBasic(prompt string) (string, error) {
	fmt.Print(prompt + ": ")
	var input string
	_, err := fmt.Scanln(&input)
	return input, err
}

// ---------------------------------------------------------------------------
// Spinner (bubbletea)

// spinnerModel renders "<spinner> title (elapsed)" until it receives a
// spinnerDoneMsg. The work runs in a goroutine owned by SpinWithFunc; Ctrl+C
// detaches the UI but the work keeps running to completion.
type spinnerModel struct {
	spin  spinner.Model
	title string
	start time.Time
}

type spinnerDoneMsg struct{}

type spinnerTickMsg time.Time

func newSpinnerModel(title string) spinnerModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
	return spinnerModel{spin: s, title: title, start: time.Now()}
}

func (m spinnerModel) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, spinnerTick())
}

func spinnerTick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return spinnerTickMsg(t) })
}

func (m spinnerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case spinnerDoneMsg:
		return m, tea.Quit
	case spinnerTickMsg:
		return m, spinnerTick()
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		return m, nil
	default:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	}
}

var spinnerElapsedStyle = lipgloss.NewStyle().Faint(true)

func (m spinnerModel) View() tea.View {
	elapsed := time.Since(m.start).Round(time.Second)
	return tea.NewView(fmt.Sprintf("%s%s %s", m.spin.View(), m.title, spinnerElapsedStyle.Render(fmt.Sprintf("(%s)", elapsed))))
}

// Spin displays a spinner while executing a command.
// The spinner automatically stops when the command completes.
func Spin(title, command string, args ...string) error {
	return SpinWithFunc(title, func() error {
		cmd := common.Command(command, args...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	})
}

// SpinWithOutput displays a spinner and captures command output.
// Returns the output and any error.
func SpinWithOutput(title, command string, args ...string) (string, error) {
	var output []byte
	err := SpinWithFunc(title, func() error {
		cmd := common.Command(command, args...)
		var err error
		output, err = cmd.CombinedOutput()
		return err
	})
	return string(output), err
}

// SpinWithFunc runs a Go function under a live spinner (with elapsed time).
// Off-terminal it simply runs the function. The function's error is always
// returned; UI failures never mask it.
func SpinWithFunc(title string, fn func() error) error {
	if !isInteractive() {
		return fn()
	}

	// The spinner renders on stderr so any stdout the work produces stays
	// machine-readable.
	prog := tea.NewProgram(newSpinnerModel(title), tea.WithOutput(os.Stderr))

	errCh := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				errCh <- fmt.Errorf("panic in spinner task: %v", r)
			}
			prog.Send(spinnerDoneMsg{})
		}()
		errCh <- fn()
	}()

	if _, uiErr := prog.Run(); uiErr != nil {
		// UI failed to start/run — the work is still going; wait for it.
		return <-errCh
	}
	return <-errCh
}

// ---------------------------------------------------------------------------
// Styling (lipgloss)

// Style renders text with the requested styling. Off-terminal it returns the
// text unchanged.
func Style(text string, opts StyleOptions) string {
	if !isInteractive() {
		return text
	}

	style := lipgloss.NewStyle()
	if opts.Foreground != "" {
		style = style.Foreground(lipgloss.Color(opts.Foreground))
	}
	if opts.Background != "" {
		style = style.Background(lipgloss.Color(opts.Background))
	}
	if opts.Bold {
		style = style.Bold(true)
	}
	if opts.Italic {
		style = style.Italic(true)
	}
	switch opts.Border {
	case "normal":
		style = style.Border(lipgloss.NormalBorder())
	case "rounded":
		style = style.Border(lipgloss.RoundedBorder())
	case "double":
		style = style.Border(lipgloss.DoubleBorder())
	case "thick":
		style = style.Border(lipgloss.ThickBorder())
	}
	return style.Render(text)
}

// SelectNamespace prompts the user to select a Kubernetes namespace
// If includeAllOption is true, adds "(all namespaces)" as the first option
// Returns empty string if "(all namespaces)" is selected
func SelectNamespace(prompt string, includeAllOption bool) (string, error) {
	namespaces, err := GetNamespaces()
	if err != nil {
		return "", err
	}

	// Add "all namespaces" option if requested
	if includeAllOption {
		namespaces = append([]string{"(all namespaces)"}, namespaces...)
	}

	// Use interactive selector
	selectedNS, err := Choose(prompt, namespaces)
	if err != nil {
		if IsCancellation(err) {
			return "", err
		}
		return "", fmt.Errorf("namespace selection failed: %w", err)
	}

	// Return empty string for "all namespaces"
	if selectedNS == "(all namespaces)" {
		return "", nil
	}

	return selectedNS, nil
}

// GetNamespaces returns the list of Kubernetes namespaces
func GetNamespaces() ([]string, error) {
	cmd := common.Command("kubectl", "get", "namespaces", "-o", "jsonpath={.items[*].metadata.name}")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get namespaces: %w", err)
	}

	namespaces := strings.Fields(string(output))
	if len(namespaces) == 0 {
		return nil, fmt.Errorf("no namespaces found in cluster")
	}

	return namespaces, nil
}

// RunWithSpinner runs a function with a spinner if verbose mode is disabled,
// otherwise runs it directly with full logger output
func RunWithSpinner(title string, verbose bool, logger interface {
	SetQuiet(bool)
	Info(string, ...interface{})
}, fn func() error) error {
	if verbose {
		// In verbose mode, show the title and run without spinner
		logger.Info("%s", title)
		return fn()
	}
	// In normal mode, use spinner and suppress logger output
	return SpinWithFunc(title, func() error {
		logger.SetQuiet(true)
		defer func() { logger.SetQuiet(false) }()
		return fn()
	})
}

// ResetTerminal resets terminal state to prevent escape code leakage.
// bubbletea restores the terminal itself; this remains as a belt-and-braces
// cleanup for code paths that ran external full-screen commands.
func ResetTerminal() {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		time.Sleep(50 * time.Millisecond)
		return
	}
	defer func() { _ = tty.Close() }()

	resetTerminal(tty)
}

func resetTerminal(tty *os.File) {
	if tty != nil {
		resetCmd := common.Command("stty", "sane")
		resetCmd.Stdin = tty
		resetCmd.Stdout = tty
		resetCmd.Stderr = tty
		_ = resetCmd.Run()

		_, _ = tty.WriteString("\033[0m\033[?25h\r")
		_ = tty.Sync()
	}

	time.Sleep(50 * time.Millisecond)
}

// IsInteractive reports whether rich TUI prompts can run (real terminals on
// stdin/stdout and interactivity not disabled). Exported for commands that
// must decide between prompting and requiring flags.
func IsInteractive() bool {
	return isInteractive()
}
