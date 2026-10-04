package server

import (
	"errors"

	"github.com/ncruces/zenity"

	"github.com/demogest/medialib/internal/players"
)

// pickFolder shows the system's folder dialog. An empty path means the user cancelled.
func pickFolder(title string) (string, error) {
	go players.FocusTitled(title)
	p, err := zenity.SelectFile(zenity.Directory(), zenity.Title(title))
	if errors.Is(err, zenity.ErrCanceled) {
		return "", nil
	}
	return p, err
}
