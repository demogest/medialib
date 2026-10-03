//go:build desktop && windows

package main

import (
	"crypto/sha1"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/demogest/medialib/internal/config"
)

// The native Windows window: the system's WebView2 (Edge) runtime, driven from pure Go, so the app needs no C
// compiler to build and no extra DLL to ship. On top of the web view this file adds what makes it feel like an
// application rather than a browser tab:
//
//   - one window per user: starting it again brings the running one to the front
//   - the window comes back where it was, at the size it had, maximized or not
//   - the title bar takes the colours of the page (and its light/dark theme)
//   - links to other sites, target=_blank and window.open go to the default browser, never to a stray window
//   - the application icon on the taskbar, and an AppUserModelID so a pinned shortcut keeps its icon

const (
	appTitle      = "Media Library"
	appUserModel  = "Demogest.MediaLibrary"
	windowClass   = "webview" // the class go-webview2 registers
	defaultWidth  = 1360
	defaultHeight = 860
)

// A desktop build opens its window when it is started without arguments (a double click).
const defaultCommand = "desktop"

// desktopBuild: this build updates from the desktop downloads.
const desktopBuild = true

//go:embed winres/icon-256.png
var icon256 []byte

//go:embed winres/icon-32.png
var icon32 []byte

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	dwmapi   = windows.NewLazySystemDLL("dwmapi.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	findWin  = user32.NewProc("FindWindowW")
	isIconic = user32.NewProc("IsIconic")
	showWin  = user32.NewProc("ShowWindow")
	setFore  = user32.NewProc("SetForegroundWindow")
	getPlace = user32.NewProc("GetWindowPlacement")
	setPlace = user32.NewProc("SetWindowPlacement")
	setLongP = user32.NewProc("SetWindowLongPtrW")
	callProc = user32.NewProc("CallWindowProcW")
	sendMsg  = user32.NewProc("SendMessageW")
	postMsg  = user32.NewProc("PostMessageW")
	metrics  = user32.NewProc("GetSystemMetrics")
	dpiSys   = user32.NewProc("GetDpiForSystem")
	monFrom  = user32.NewProc("MonitorFromRect")
	mkIcon   = user32.NewProc("CreateIconFromResourceEx")
	dwmAttr  = dwmapi.NewProc("DwmSetWindowAttribute")
	setAppID = shell32.NewProc("SetCurrentProcessExplicitAppUserModelID")
)

type point struct{ X, Y int32 }
type rect struct{ Left, Top, Right, Bottom int32 }
type placement struct {
	Length, Flags, ShowCmd uint32
	MinPos, MaxPos         point
	Normal                 rect
}

// The manifest asks for per-monitor DPI awareness; this makes sure of it when the exe is run in odd ways (a
// resource that did not link, a debugger). It has to happen before any window exists.
func init() {
	user32.NewProc("SetProcessDpiAwarenessContext").Call(^uintptr(3)) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = -4
}

// savedWindow is window.json in the config folder.
type savedWindow struct {
	X, Y, W, H int32
	Maximized  bool
}

func windowFile() string { return filepath.Join(config.Home(), "window.json") }

// desktopPreflight runs before the server starts. A second copy of the app has nothing to do but wake the first.
func desktopPreflight() bool {
	sum := sha1.Sum([]byte(config.Home()))
	name, _ := windows.UTF16PtrFromString(`Local\medialib-desktop-` + hex.EncodeToString(sum[:6]))
	_, err := windows.CreateMutex(nil, false, name) // held until the process ends
	if err == windows.ERROR_ALREADY_EXISTS {
		focusRunningWindow()
		return false
	}
	return true
}

func focusRunningWindow() {
	class, _ := windows.UTF16PtrFromString(windowClass)
	title, _ := windows.UTF16PtrFromString(appTitle)
	hwnd, _, _ := findWin.Call(uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title)))
	if hwnd == 0 {
		return
	}
	if r, _, _ := isIconic.Call(hwnd); r != 0 {
		showWin.Call(hwnd, 9) // SW_RESTORE
	}
	setFore.Call(hwnd)
}

func webView2Installed() bool {
	const key = `Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`
	for _, c := range []struct {
		root registry.Key
		path string
	}{
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\` + key},
		{registry.LOCAL_MACHINE, `SOFTWARE\` + key},
		{registry.CURRENT_USER, `Software\` + key},
	} {
		k, err := registry.OpenKey(c.root, c.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		v, _, err := k.GetStringValue("pv")
		k.Close()
		if err == nil && v != "" && v != "0.0.0.0" {
			return true
		}
	}
	return false
}

func systemUsesDarkApps() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	return err == nil && v == 0
}

// pageScript runs in every page before its own scripts. It keeps links out of stray windows and reports the page's
// background colour so the title bar can follow it.
const pageScript = `(() => {
  const cross = u => { try { return new URL(u, location.href).origin !== location.origin; } catch (e) { return false; } };
  const out = u => window.__medialib_open(new URL(u, location.href).href);
  window.open = u => { if (u) out(u); return null; };
  const link = e => {
    const a = e.target && e.target.closest && e.target.closest('a[href]');
    if (!a || a.hasAttribute('download') || e.defaultPrevented) return;
    if (a.target === '_blank' || cross(a.href)) { e.preventDefault(); out(a.href); }
  };
  document.addEventListener('click', link, true);
  document.addEventListener('auxclick', link, true);
  let last = '';
  const sync = () => {
    if (!document.body) return;
    const c = getComputedStyle(document.body).backgroundColor.match(/[\d.]+/g);
    if (!c || c.length < 3 || c.join() === last) return;
    last = c.join();
    window.__medialib_chrome(Math.round(c[0]), Math.round(c[1]), Math.round(c[2]));
  };
  addEventListener('DOMContentLoaded', () => {
    sync();
    new MutationObserver(sync).observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme', 'class'] });
    matchMedia('(prefers-color-scheme: dark)').addEventListener('change', sync);
    setTimeout(sync, 250);
  });
  addEventListener('load', sync);
})();`

// runDesktop shows the UI in a native window and returns when it is closed.
func runDesktop(url string) error {
	if !webView2Installed() {
		log.Println("The WebView2 runtime is not installed; using an Edge/Chrome app window instead.")
		return runAppMode(url)
	}
	// Paint the web view in the page's own colour while the first page loads, so there is no white flash at night.
	if os.Getenv("WEBVIEW2_DEFAULT_BACKGROUND_COLOR") == "" {
		bg := "FFF5F6F8"
		if systemUsesDarkApps() {
			bg = "FF0F1114"
		}
		os.Setenv("WEBVIEW2_DEFAULT_BACKGROUND_COLOR", bg)
	}
	setAppID.Call(uintptr(unsafe.Pointer(utf16(appUserModel))))

	dpi, _, _ := dpiSys.Call()
	if dpi == 0 {
		dpi = 96
	}
	sw, _, _ := metrics.Call(0) // SM_CXSCREEN
	sh, _, _ := metrics.Call(1) // SM_CYSCREEN
	width := min(uint(defaultWidth)*uint(dpi)/96, uint(sw)*92/100)
	height := min(uint(defaultHeight)*uint(dpi)/96, uint(sh)*92/100)

	debug := os.Getenv("MEDIALIB_DEBUG") != ""
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     debug,
		AutoFocus: true,
		DataPath:  filepath.Join(config.Home(), "webview2"),
		WindowOptions: webview2.WindowOptions{
			Title: appTitle, Width: width, Height: height, Center: true,
		},
	})
	if w == nil {
		log.Println("Could not start WebView2; using an Edge/Chrome app window instead.")
		return runAppMode(url)
	}
	defer w.Destroy()
	hwnd := uintptr(w.Window())
	hideStatusBubble(w)

	setIcons(hwnd)
	restoreWindow(hwnd, int32(width), int32(height), int32(sw), int32(sh))
	subclass(hwnd)
	w.SetSize(int(width/2), int(height/2), webview2.HintMin) // never smaller than half the first size

	closeWindow = func() { postMsg.Call(hwnd, 0x0010, 0, 0) } // WM_CLOSE: the window remembers its place, as when closed by hand
	_ = w.Bind("__medialib_open", func(u string) { openExternal(u) })
	_ = w.Bind("__medialib_chrome", func(r, g, b int) { colorTitleBar(hwnd, r, g, b) })
	w.Init(pageScript)
	w.Navigate(url)
	w.Run()
	return nil
}

func utf16(s string) *uint16 { p, _ := windows.UTF16PtrFromString(s); return p }

// openExternal hands http(s) links to the default browser; anything else is ignored.
func openExternal(u string) {
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		openBrowser(u)
	}
}

// setIcons gives the window its taskbar and title-bar icons, from the PNGs embedded in the exe.
func setIcons(hwnd uintptr) {
	set := func(kind uintptr, png []byte, metric uintptr) {
		size, _, _ := metrics.Call(metric)
		h, _, _ := mkIcon.Call(uintptr(unsafe.Pointer(&png[0])), uintptr(len(png)), 1, 0x00030000, size, size, 0)
		if h != 0 {
			sendMsg.Call(hwnd, 0x0080, kind, h) // WM_SETICON
		}
	}
	set(1, icon256, 11) // ICON_BIG, SM_CXICON
	set(0, icon32, 49)  // ICON_SMALL, SM_CXSMICON
}

func setAttr(hwnd uintptr, attr uintptr, v uint32) {
	dwmAttr.Call(hwnd, attr, uintptr(unsafe.Pointer(&v)), 4)
}

// colorTitleBar paints the caption and border in the page's background and picks light or dark caption text
// (Windows 11; older versions ignore the colours but still honour the dark flag where they know it).
func colorTitleBar(hwnd uintptr, r, g, b int) {
	dark := 0.2126*float64(r)+0.7152*float64(g)+0.0722*float64(b) < 128
	flag := uint32(0)
	text := uint32(0x00191919)
	if dark {
		flag, text = 1, 0x00F2F2F2
	}
	col := uint32(b)<<16 | uint32(g)<<8 | uint32(r) // COLORREF is 0x00BBGGRR
	setAttr(hwnd, 20, flag)                         // DWMWA_USE_IMMERSIVE_DARK_MODE
	setAttr(hwnd, 35, col)                          // DWMWA_CAPTION_COLOR
	setAttr(hwnd, 34, col)                          // DWMWA_BORDER_COLOR
	setAttr(hwnd, 36, text)                         // DWMWA_TEXT_COLOR
}

// restoreWindow puts the window back where it was, unless that place is off every screen now.
func restoreWindow(hwnd uintptr, w, h, screenW, screenH int32) {
	// The first run, or a saved place that is off every screen now: the middle of the primary screen.
	s := savedWindow{X: (screenW - w) / 2, Y: (screenH - h) / 2, W: w, H: h}
	if data, err := os.ReadFile(windowFile()); err == nil {
		var saved savedWindow
		if json.Unmarshal(data, &saved) == nil && saved.W >= 300 && saved.H >= 200 {
			r := rect{saved.X, saved.Y, saved.X + saved.W, saved.Y + saved.H}
			if m, _, _ := monFrom.Call(uintptr(unsafe.Pointer(&r)), 0); m != 0 { // MONITOR_DEFAULTTONULL
				s = saved
			}
		}
	}
	r := rect{s.X, s.Y, s.X + s.W, s.Y + s.H}
	p := placement{Normal: r, ShowCmd: 1}
	p.Length = uint32(unsafe.Sizeof(p))
	if s.Maximized {
		p.ShowCmd = 3
	}
	setPlace.Call(hwnd, uintptr(unsafe.Pointer(&p)))
}

func saveWindow(hwnd uintptr) {
	p := placement{}
	p.Length = uint32(unsafe.Sizeof(p))
	if r, _, _ := getPlace.Call(hwnd, uintptr(unsafe.Pointer(&p))); r == 0 {
		return
	}
	s := savedWindow{X: p.Normal.Left, Y: p.Normal.Top, W: p.Normal.Right - p.Normal.Left, H: p.Normal.Bottom - p.Normal.Top, Maximized: p.ShowCmd == 3}
	if data, err := json.Marshal(s); err == nil {
		_ = os.WriteFile(windowFile(), data, 0o600)
	}
}

// subclass watches the window for WM_CLOSE to remember its place, then lets go-webview2's own handler carry on.
func subclass(hwnd uintptr) {
	var old uintptr
	cb := windows.NewCallback(func(h, msg, wp, lp uintptr) uintptr {
		if msg == 0x0010 { // WM_CLOSE
			saveWindow(h)
		}
		r, _, _ := callProc.Call(old, h, msg, wp, lp)
		return r
	})
	old, _, _ = setLongP.Call(hwnd, ^uintptr(3), cb) // GWLP_WNDPROC = -4
}

// hideStatusBubble turns off the URL that WebView2 floats in the corner while the pointer is over a link. The
// library does not expose the setting, so it is reached through the web view it wraps.
func hideStatusBubble(w webview2.WebView) {
	f := reflect.ValueOf(w).Elem().FieldByName("browser")
	if !f.IsValid() {
		return
	}
	b := reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem().Interface()
	if g, ok := b.(interface {
		GetSettings() (*edge.ICoreWebViewSettings, error)
	}); ok {
		if st, err := g.GetSettings(); err == nil {
			_ = st.PutIsStatusBarEnabled(false)
		}
	}
}
