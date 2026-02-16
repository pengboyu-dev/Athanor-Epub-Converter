package main

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

func main() {
	// ── App-level context: cancelled when the window closes ──
	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()

	// ── Fyne application ──
	a := app.NewWithID("com.tools.epub-converter")
	w := a.NewWindow("EPUB Converter")
	w.Resize(fyne.NewSize(620, 400))
	w.SetFixedSize(true)
	w.CenterOnScreen()

	// Kill child processes on window close.
	w.SetCloseIntercept(func() {
		appCancel()
		a.Quit()
	})

	// ── UI widgets ──
	titleLabel := widget.NewLabelWithStyle(
		"EPUB Converter",
		fyne.TextAlignCenter,
		fyne.TextStyle{Bold: true},
	)
	subtitleLabel := widget.NewLabelWithStyle(
		"EPUB  →  Markdown (AI)  +  PDF (Human)",
		fyne.TextAlignCenter,
		fyne.TextStyle{Italic: true},
	)

	fileLabel := widget.NewLabel("No file selected")
	fileLabel.Alignment = fyne.TextAlignCenter
	fileLabel.Wrapping = fyne.TextWrapWord

	mdStatus := widget.NewLabel("")
	pdfStatus := widget.NewLabel("")

	progress := widget.NewProgressBarInfinite()
	progress.Hide()

	var busy bool // simple guard; only touched on the main goroutine path

	selectBtn := widget.NewButtonWithIcon(
		"Select EPUB & Convert",
		theme.FolderOpenIcon(),
		nil, // assigned below
	)
	selectBtn.Importance = widget.HighImportance

	// ── Button action ──
	selectBtn.OnTapped = func() {
		if busy {
			return
		}

		fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if err != nil {
				dialog.ShowError(err, w)
				return
			}
			if reader == nil {
				return // user cancelled the dialog
			}

			epubURI := reader.URI()
			_ = reader.Close()

			filePath := uriToPath(epubURI)

			// ── Update UI state ──
			fileLabel.SetText("📄 " + filepath.Base(filePath))
			mdStatus.SetText("⏳  Markdown — converting …")
			pdfStatus.SetText("⏳  PDF — converting …")
			progress.Show()
			selectBtn.Disable()
			busy = true

			// ── Run both conversions in background ──
			go func() {
				var wg sync.WaitGroup
				wg.Add(2)

				go func() {
					defer wg.Done()
					if err := convertToMarkdown(appCtx, filePath); err != nil {
						mdStatus.SetText("❌  Markdown — " + trimMsg(err.Error(), 90))
					} else {
						mdStatus.SetText("✅  Markdown — done")
					}
				}()

				go func() {
					defer wg.Done()
					if err := convertToPDF(appCtx, filePath); err != nil {
						pdfStatus.SetText("❌  PDF — " + trimMsg(err.Error(), 90))
					} else {
						pdfStatus.SetText("✅  PDF — done")
					}
				}()

				wg.Wait()

				// ── Conversion finished ──
				progress.Hide()
				selectBtn.Enable()
				busy = false

				dir := filepath.Dir(filePath)
				allOK := strings.HasPrefix(mdStatus.Text, "✅") &&
					strings.HasPrefix(pdfStatus.Text, "✅")

				if allOK {
					a.SendNotification(&fyne.Notification{
						Title:   "EPUB Converter",
						Content: "Conversion complete!  Files in: " + dir,
					})
					dialog.ShowInformation("Done",
						"All output files saved to:\n"+dir, w)
				} else {
					dialog.ShowError(
						fmt.Errorf("one or more conversions failed — see status"), w)
				}
			}()
		}, w)

		fd.SetFilter(storage.NewExtensionFileFilter([]string{".epub"}))
		fd.Show()
	}

	// ── Check external tool availability (non-blocking) ──
	go func() {
		if missing := checkDependencies(); len(missing) > 0 {
			msg := fmt.Sprintf(
				"Required tools not found in PATH:\n  • %s\n\n"+
					"Please install them before converting.",
				strings.Join(missing, "\n  • "),
			)
			dialog.ShowError(fmt.Errorf("%s", msg), w)
		}
	}()

	// ── Layout ──
	header := container.NewVBox(titleLabel, subtitleLabel)
	statusArea := container.NewVBox(
		widget.NewSeparator(),
		mdStatus,
		pdfStatus,
		progress,
	)
	content := container.NewVBox(
		header,
		widget.NewSeparator(),
		layout.NewSpacer(),
		container.NewCenter(selectBtn),
		layout.NewSpacer(),
		fileLabel,
		statusArea,
	)

	w.SetContent(container.NewPadded(content))
	w.ShowAndRun()
}

// ──────────────────────────── helpers ────────────────────────────

// uriToPath converts a Fyne URI to a native file-system path.
// On Windows, fyne returns "/C:/…" — we strip the leading slash.
func uriToPath(u fyne.URI) string {
	p := u.Path()
	if runtime.GOOS == "windows" && len(p) > 2 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return p
}

// trimMsg returns s truncated to maxLen runes with "…" appended.
func trimMsg(s string, maxLen int) string {
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen]) + "…"
}

// checkDependencies verifies that pandoc and xelatex are on the PATH.
func checkDependencies() []string {
	var missing []string
	if _, err := exec.LookPath("pandoc"); err != nil {
		missing = append(missing, "pandoc")
	}
	if _, err := exec.LookPath("xelatex"); err != nil {
		missing = append(missing, "xelatex (TinyTeX)")
	}
	return missing
}
