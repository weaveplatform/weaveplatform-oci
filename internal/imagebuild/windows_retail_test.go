package imagebuild

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestRetailSDKResolution(t *testing.T) {
	for _, mode := range []string{"amd64", "arm64", "catalogue", "missing-language", "page", "wrong-language", "size", "wrong-release"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				arch, token := "amd64", "x64"
				if mode == "arm64" {
					arch, token = "arm64", "ARM64"
				}
				selection := WindowsSelection{"pro-26h2", arch, "en-US"}
				uri := "https://software.download.prss.microsoft.com/Win11_26H2_English_" + token + ".iso"
				if mode == "wrong-release" {
					uri = strings.Replace(uri, "26H2", "25H2", 1)
				}
				probes := 0
				p := Packages{
					Downloader: Downloader{
						Client: &http.Client{
							Timeout: time.Second,
							Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
								body := ""
								status := 200
								size := int64(-1)
								switch {
								case r.URL.Host == "go.microsoft.com":
									locale := "en-us"
									if mode == "missing-language" {
										locale = "de-de"
									}
									xml := fmt.Sprintf(
										`<MCT><Catalogs><Catalog><PublishedMedia><Files><File><FileName>26200.1.release_CLIENTBUSINESS_VOL_X64FRE_en-us.esd</FileName><Edition>Enterprise</Edition><Language>English (United States)</Language><LanguageCode>%s</LanguageCode><Architecture>x64</Architecture></File></Files></PublishedMedia></Catalog></Catalogs></MCT>`,
										locale,
									)
									body = string(storeCAB([]byte(xml)))
									if mode == "catalogue" {
										body = "invalid cabinet"
									}
								case strings.Contains(r.URL.Path, "getskuinformationbyproductedition"):
									language := "English (United States)"
									if mode == "wrong-language" {
										language = "English (United States) Other"
									}
									body = fmt.Sprintf(
										`{"Skus":[{"Id":"123","Language":"English","LocalizedLanguage":%q}]}`,
										language,
									)
								case strings.Contains(r.URL.Path, "GetProductDownloadLinksBySku"):
									body = fmt.Sprintf(
										`{"ProductDownloadOptions":[{"Uri":%q}]}`,
										uri,
									)
								case strings.HasPrefix(r.URL.Host, "software.download"):
									if r.Method != http.MethodHead {
										t.Fatal("resolution downloaded ISO")
									}
									probes++
									size = 512
									if mode == "size" {
										size = -1
									}
								case strings.Contains(r.URL.Path, "software-download/windows11"):
									body = fmt.Sprintf(
										`<select id="product-edition"><option value="123">Windows 11 (multi-edition ISO for %s devices)</option></select>`,
										token,
									)
									if mode == "page" {
										body = "no editions"
									}
								case r.URL.Host == "vlscppe.microsoft.com" || r.URL.Host == "ov-df.microsoft.com":
								default:
									t.Fatal("unexpected SDK endpoint", r.URL)
								}
								return &http.Response{
									StatusCode:    status,
									Request:       r,
									Header:        http.Header{},
									Body:          io.NopCloser(strings.NewReader(body)),
									ContentLength: size,
								}, nil
							}),
						},
					},
				}
				source, err := p.ResolveWindows(t.Context(), selection)
				if mode == "amd64" || mode == "arm64" {
					must(t, err)
					if source.Arch != arch || source.Language != "en-US" || source.Size != 512 ||
						source.Release != "26h2" ||
						probes != 1 {
						t.Fatal(source, probes)
					}
				} else if err == nil {
					t.Fatal("accepted " + mode)
				}
			})
		})
	}
}

func TestRetailSizeProbeFailure(t *testing.T) {
	_, source := windowsFixture()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (Packages{}).windowsMediaSize(ctx, source.URI); err == nil {
		t.Fatal("cancelled size probe")
	}
	if _, err := (Packages{}).windowsMediaSize(
		t.Context(),
		"https://example.com/media.iso",
	); err == nil {
		t.Fatal("untrusted size probe")
	}
	p := Packages{
		Downloader: Downloader{
			Client: &http.Client{
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode:    403,
						Request:       r,
						Header:        http.Header{},
						Body:          io.NopCloser(strings.NewReader("")),
						ContentLength: 512,
					}, nil
				}),
			},
		},
	}
	if _, err := p.windowsMediaSize(t.Context(), source.URI); err == nil {
		t.Fatal("accepted failed size probe")
	}
}
