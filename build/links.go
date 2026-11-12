package build

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

type linkUse struct{ out, link string }

func (p *Project) checkExternal(records map[string]*OutRecord) {
	siblings := p.siblingURLs()
	uses := map[string][]linkUse{}
	for _, out := range sortedKeys(records) {
		for _, l := range records[out].Scan.Links {
			if !strings.HasPrefix(l, "https://") || externalLinkProblem(l) != "" {
				continue
			}
			h := hostOf(l)
			if _, ours := siblings["https://"+h]; ours || p.ownHost(h) {
				continue
			}
			u, _, _ := strings.Cut(l, "#")
			uses[u] = append(uses[u], linkUse{out, l})
		}
	}
	hosts := map[string][]string{}
	for _, u := range sortedKeys(uses) {
		hosts[hostOf(u)] = append(hosts[hostOf(u)], u)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	slots := make(chan struct{}, 64)
	var mu sync.Mutex
	var unverified []string
	broken := 0
	var wg sync.WaitGroup
	for _, list := range hosts {
		jobs := make(chan string, len(list))
		for _, u := range list {
			jobs <- u
		}
		close(jobs)
		for range min(4, len(list)) {
			wg.Go(func() {
				for u := range jobs {
					slots <- struct{}{}
					status, err := fetchStatus(client, u)
					<-slots
					var dns *net.DNSError
					switch {
					case err == nil && status < 300:
					case err == nil && (status == http.StatusNotFound || status == http.StatusGone):
						mu.Lock()
						broken++
						mu.Unlock()
						for _, use := range uses[u] {
							p.report(at{out: use.out}, findLink, use.link, "external link %s answers %d; link to a page that exists or remove the link", use.link, status)
						}
					case errors.As(err, &dns) && dns.IsNotFound:
						mu.Lock()
						broken++
						mu.Unlock()
						for _, use := range uses[u] {
							p.report(at{out: use.out}, findLink, use.link, "external link %s points to a host that does not exist; fix or remove the link", use.link)
						}
					default:
						why := fmt.Sprintf("status %d", status)
						if err != nil {
							why = shortError(err)
						}
						mu.Lock()
						unverified = append(unverified, fmt.Sprintf("  %s (%s), on %s", u, why, pageOf(uses[u][0].out)))
						mu.Unlock()
					}
				}
			})
		}
	}
	wg.Wait()
	slices.Sort(unverified)
	fmt.Fprintf(os.Stderr, "checked %d external links on %d hosts: %d broken, %d could not be verified\n", len(uses), len(hosts), broken, len(unverified))
	if len(unverified) > 0 {
		fmt.Fprintf(os.Stderr, "not verified (the server refused the check, failed or timed out; open these by hand):\n%s\n", strings.Join(unverified, "\n"))
	}
}

func pageOf(out string) string {
	return "/" + strings.TrimSuffix(out, "index.html")
}

func shortError(err error) string {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timed out"
	}
	msg := err.Error()
	if i := strings.LastIndex(msg, ": "); i >= 0 {
		msg = msg[i+2:]
	}
	return msg
}

func fetchStatus(client *http.Client, u string) (int, error) {
	var status int
	var err error
	for attempt := range 2 {
		if attempt > 0 {
			time.Sleep(3 * time.Second)
		}
		status, err = fetchOnce(client, u)
		if err == nil && status != http.StatusTooManyRequests && status < 500 {
			break
		}
		var dns *net.DNSError
		if errors.As(err, &dns) && dns.IsNotFound {
			break
		}
	}
	return status, err
}

func fetchOnce(client *http.Client, u string) (int, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; lyn-link-check; +https://github.com/beint-no/lyn)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	res, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	res.Body.Close()
	return res.StatusCode, nil
}
