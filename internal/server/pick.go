package server

import (
	"errors"

	"github.com/ncruces/zenity"

	"github.com/demogest/medialib/internal/players"
)

const pickTitle = "Choose a media folder"

// pickFolder shows the system's folder dialog. An empty path means the user cancelled.
func pickFolder() (string, error) {
	go players.FocusTitled(pickTitle)
	p, err := zenity.SelectFile(zenity.Directory(), zenity.Title(pickTitle))
	if errors.Is(err, zenity.ErrCanceled) {
		return "", nil
	}
	return p, err
}
