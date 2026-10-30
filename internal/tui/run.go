package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
)

// Run shows the interface until the user quits. A first run starts at once.
func Run(ctx context.Context, opts Options) error {
	m := newModel(ctx, opts)
	m.splash = true
	m.program = tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := m.program.Run()
	return err
}
