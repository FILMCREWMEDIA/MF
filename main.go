// Monitor Facturare - local desktop launcher.
// No self-extraction, no PowerShell, no downloaded components.
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "io"
    "net"
    "net/http"
    "os"
    "os/exec"
    "path/filepath"
    "strings"
    "syscall"
    "time"
    "unsafe"
)

var (
    comdlg32 = syscall.NewLazyDLL("comdlg32.dll")
    procGetSaveFileNameW = comdlg32.NewProc("GetSaveFileNameW")
)

type openFileName struct {
    lStructSize       uint32
    hwndOwner         uintptr
    hInstance         uintptr
    lpstrFilter       *uint16
    lpstrCustomFilter *uint16
    nMaxCustFilter    uint32
    nFilterIndex      uint32
    lpstrFile         *uint16
    nMaxFile          uint32
    lpstrFileTitle    *uint16
    nMaxFileTitle     uint32
    lpstrInitialDir   *uint16
    lpstrTitle        *uint16
    flags             uint32
    nFileOffset       uint16
    nFileExtension    uint16
    lpstrDefExt       *uint16
    lCustData         uintptr
    lpfnHook          uintptr
    lpTemplateName    *uint16
    pvReserved        uintptr
    dwReserved        uint32
    flagsEx           uint32
}

const (
    ofnOverwritePrompt = 0x00000002
    ofnPathMustExist   = 0x00000800
    ofnNoChangedir     = 0x00000008
)

func utf16ptr(s string) *uint16 {
    p, _ := syscall.UTF16PtrFromString(s)
    return p
}

func nativeSave(name, ext string, data []byte) (string, bool, error) {
    filter := "PDF (*.pdf)\x00*.pdf\x00Excel (*.xlsx)\x00*.xlsx\x00Toate fișierele (*.*)\x00*.*\x00\x00"
    if strings.EqualFold(ext, ".xlsx") {
        filter = "Excel (*.xlsx)\x00*.xlsx\x00Toate fișierele (*.*)\x00*.*\x00\x00"
    } else if strings.EqualFold(ext, ".pdf") {
        filter = "PDF (*.pdf)\x00*.pdf\x00Toate fișierele (*.*)\x00*.*\x00\x00"
    }
    fileBuf := make([]uint16, 32768)
    copy(fileBuf, mustUTF16(name))
    ofn := openFileName{
        lStructSize: uint32(unsafe.Sizeof(openFileName{})),
        lpstrFilter: utf16ptr(filter),
        nFilterIndex: 1,
        lpstrFile: &fileBuf[0],
        nMaxFile: uint32(len(fileBuf)),
        lpstrTitle: utf16ptr("Salvează document"),
        flags: ofnOverwritePrompt | ofnPathMustExist | ofnNoChangedir,
        lpstrDefExt: utf16ptr(strings.TrimPrefix(ext, ".")),
    }
    r, _, callErr := procGetSaveFileNameW.Call(uintptr(unsafe.Pointer(&ofn)))
    if r == 0 {
        // 0 with no error is a normal Cancel. CommDlgExtendedError can be checked,
        // but we intentionally treat it as cancel to avoid a false alarm.
        _ = callErr
        return "", false, nil
    }
    path := syscall.UTF16ToString(fileBuf)
    if !strings.HasSuffix(strings.ToLower(path), strings.ToLower(ext)) {
        path += ext
    }
    if err := os.WriteFile(path, data, 0644); err != nil {
        return "", false, err
    }
    return path, true, nil
}

func mustUTF16(s string) []uint16 {
    p, _ := syscall.UTF16FromString(s)
    return p
}

type saveResponse struct {
    OK        bool   `json:"ok"`
    Cancelled bool   `json:"cancelled,omitempty"`
    Path      string `json:"path,omitempty"`
    Error     string `json:"error,omitempty"`
}

func saveHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost { http.Error(w, "method", http.StatusMethodNotAllowed); return }
    defer r.Body.Close()
    data, err := io.ReadAll(io.LimitReader(r.Body, 100<<20))
    if err != nil { http.Error(w, err.Error(), 500); return }
    name := r.URL.Query().Get("name")
    ext := r.URL.Query().Get("ext")
    if name == "" { name = "monitor_facturare" + ext }
    if ext == "" { ext = filepath.Ext(name) }
    path, ok, err := nativeSave(name, ext, data)
    w.Header().Set("Content-Type", "application/json; charset=utf-8")
    if err != nil { _ = json.NewEncoder(w).Encode(saveResponse{Error: err.Error()}); return }
    if !ok { _ = json.NewEncoder(w).Encode(saveResponse{Cancelled:true}); return }
    _ = json.NewEncoder(w).Encode(saveResponse{OK:true, Path:path})
}

func findBrowser() (string, []string) {
    local := os.Getenv("LOCALAPPDATA")
    pf := os.Getenv("ProgramFiles")
    pfx86 := os.Getenv("ProgramFiles(x86)")
    candidates := []string{
        filepath.Join(local, "Microsoft", "Edge", "Application", "msedge.exe"),
        filepath.Join(pf, "Microsoft", "Edge", "Application", "msedge.exe"),
        filepath.Join(pfx86, "Microsoft", "Edge", "Application", "msedge.exe"),
        filepath.Join(local, "Google", "Chrome", "Application", "chrome.exe"),
        filepath.Join(pf, "Google", "Chrome", "Application", "chrome.exe"),
        filepath.Join(pfx86, "Google", "Chrome", "Application", "chrome.exe"),
    }
    for _, p := range candidates { if p != "" { if _, err := os.Stat(p); err == nil { return p, []string{"--app=%s"} } } }
    return "", nil
}

func main() {
    mux := http.NewServeMux()
    mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/" { http.NotFound(w,r); return }
        b, err := os.ReadFile("app.html")
        if err != nil { http.Error(w, err.Error(), 500); return }
        w.Header().Set("Content-Type", "text/html; charset=utf-8")
        _, _ = w.Write(b)
    })
    mux.HandleFunc("/__native_save", saveHandler)
    mux.HandleFunc("/__health", func(w http.ResponseWriter,r *http.Request){ w.WriteHeader(204) })

    ln, err := net.Listen("tcp", "127.0.0.1:0")
    if err != nil { return }
    server := &http.Server{Handler:mux, ReadHeaderTimeout:5*time.Second}
    go server.Serve(ln)
    url := "http://" + ln.Addr().String() + "/"

    if browser, args := findBrowser(); browser != "" {
        cmd := exec.Command(browser, fmt.Sprintf(args[0], url))
        _ = cmd.Start()
    } else {
        _ = exec.Command("cmd", "/c", "start", "", url).Start()
    }

    // Keep the local service alive while the desktop app is being used.
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    <-ctx.Done()
}
