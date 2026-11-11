package control

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// A full store makes room by sender, as the sign-in tables do: the newest
// waiting report of the client holding the most is pushed out, so a flood
// from a few addresses cannot keep a person's report out. The client is
// kept only as an HMAC of its key (report.Report.Client, an IPv4 address
// or an IPv6 /64) under a random secret made once per install, never as
// the address, and is forgotten once the report leaves the waiting reports.
const (
	// reportClientSecretKey holds the secret the client keys are hashed
	// under.
	reportClientSecretKey = "client-secret"
	// reportClientPrefix holds, for each client hash, the keys of the
	// client's waiting reports, oldest first.
	reportClientPrefix = "c/"
	// reportDroppedKey counts the waiting reports pushed out to make room.
	reportDroppedKey = "dropped-count"
	// maxWaitingPerClient bounds the reports one client may have waiting
	// to be published. Reports are published in the order they were
	// received, at most ReportDailyPublishBudget a day, so without it one
	// address at its hourly limit would queue a day's sending, and weeks of
	// publication, ahead of a person's report; with it a person's report
	// waits behind at most this many of any one address.
	maxWaitingPerClient = 10
	// clientBacklogRetry is the Retry-After of a report refused because its
	// client holds maxWaitingPerClient waiting reports: one of them leaves
	// as it is published, about every half hour at the daily budget.
	clientBacklogRetry = 30 * time.Minute
)

// tooManyWaiting refuses a report whose client already has
// maxWaitingPerClient reports waiting, with a time to retry.
func tooManyWaiting() error {
	err := core.NewError(core.CodeRateLimited, "too many of this client's reports wait to be published; try again later")
	err.Details = map[string]string{"retry_after_seconds": strconv.Itoa(int(clientBacklogRetry / time.Second))}
	return err
}

// waitingCount is how many reports client has waiting, read from its list
// of them alone; a report without a client counts none.
func waitingCount(ctx context.Context, tx state.Tx, client string) (int, error) {
	if client == "" {
		return 0, nil
	}
	list, err := readClientReports(ctx, tx, client)
	return len(list.keys), err
}

// clientHash returns the name the store keeps for the client key raw: an
// HMAC under the install's secret, made on first use. An empty key names
// no client.
func clientHash(ctx context.Context, tx state.Tx, raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	entry, err := tx.GetKV(ctx, ReportsNamespace, reportClientSecretKey)
	switch {
	case core.HasCode(err, core.CodeNotFound):
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return "", core.NewError(core.CodeInternal, "no randomness available").WithCause(err)
		}
		entry = state.KVEntry{Namespace: ReportsNamespace, Key: reportClientSecretKey, Value: []byte(hex.EncodeToString(secret))}
		if _, err := tx.PutKV(ctx, entry); err != nil {
			return "", err
		}
	case err != nil:
		return "", err
	}
	secret, err := hex.DecodeString(string(entry.Value))
	if err != nil || len(secret) < 32 {
		return "", core.NewError(core.CodeTampered, "the stored secret of report clients cannot be read")
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(raw))
	return hex.EncodeToString(mac.Sum(nil))[:32], nil
}

// clientReports is the list of a client's waiting reports, oldest first.
type clientReports struct {
	entry state.KVEntry
	keys  []string
}

func readClientReports(ctx context.Context, tx state.Tx, client string) (clientReports, error) {
	entry, err := tx.GetKV(ctx, ReportsNamespace, reportClientPrefix+client)
	if core.HasCode(err, core.CodeNotFound) {
		return clientReports{entry: state.KVEntry{Namespace: ReportsNamespace, Key: reportClientPrefix + client}}, nil
	}
	if err != nil {
		return clientReports{}, err
	}
	return decodeClientReports(entry)
}

func decodeClientReports(entry state.KVEntry) (clientReports, error) {
	var keys []string
	if err := json.Unmarshal(entry.Value, &keys); err != nil {
		return clientReports{}, core.NewError(core.CodeTampered, "the stored reports of a client cannot be read").WithCause(err)
	}
	return clientReports{entry: entry, keys: keys}, nil
}

// put stores the list, or removes it once empty.
func (c clientReports) put(ctx context.Context, tx state.Tx) error {
	if len(c.keys) == 0 {
		if c.entry.Version == 0 {
			return nil
		}
		return tx.DeleteKV(ctx, ReportsNamespace, c.entry.Key, c.entry.Version)
	}
	raw, err := json.Marshal(c.keys)
	if err != nil {
		return core.NewError(core.CodeInternal, "the reports of a client cannot be encoded").WithCause(err)
	}
	c.entry.Value = raw
	_, err = tx.PutKV(ctx, c.entry)
	return err
}

// addWaiting records key as the newest waiting report of client.
func addWaiting(ctx context.Context, tx state.Tx, client, key string) error {
	if client == "" {
		return nil
	}
	list, err := readClientReports(ctx, tx, client)
	if err != nil {
		return err
	}
	list.keys = append(list.keys, key)
	return list.put(ctx, tx)
}

// forgetWaiting removes key from the waiting reports of client.
func forgetWaiting(ctx context.Context, tx state.Tx, client, key string) error {
	if client == "" {
		return nil
	}
	list, err := readClientReports(ctx, tx, client)
	if err != nil {
		return err
	}
	list.keys = slices.DeleteFunc(list.keys, func(k string) bool { return k == key })
	return list.put(ctx, tx)
}

// forgetWaitingAnywhere removes key from the waiting reports of whichever
// client lists it, for a report whose value cannot be read to name its
// client. It reads the client lists, never a report.
func forgetWaitingAnywhere(ctx context.Context, tx state.Tx, key string) error {
	entries, err := tx.ListKV(ctx, ReportsNamespace, reportClientPrefix, 0)
	if err != nil {
		return err
	}
	for _, e := range entries {
		list, err := decodeClientReports(e)
		if err != nil {
			return err
		}
		if !slices.Contains(list.keys, key) {
			continue
		}
		list.keys = slices.DeleteFunc(list.keys, func(k string) bool { return k == key })
		if err := list.put(ctx, tx); err != nil {
			return err
		}
	}
	return nil
}

// forgetWaitingEntry forgets the waiting report stored at e, whose value
// names its client.
func forgetWaitingEntry(ctx context.Context, tx state.Tx, e state.KVEntry) error {
	var stored storedReport
	if json.Unmarshal(e.Value, &stored) != nil {
		return nil
	}
	return forgetWaiting(ctx, tx, stored.Client, e.Key)
}

// dropFromBusiestClient makes room for a report of newcomer by removing the
// newest waiting report of the client holding the most, as long as that
// client holds more than one and more than newcomer does. A report whose
// publication is under way is never removed; the next newest of that
// client goes instead, or else the next busiest client's. It reports
// whether it made room.
func (s storedReports) dropFromBusiestClient(ctx context.Context, tx state.Tx, newcomer string, count *reportCount) (bool, error) {
	entries, err := tx.ListKV(ctx, ReportsNamespace, reportClientPrefix, 0)
	if err != nil {
		return false, err
	}
	lists := make([]clientReports, 0, len(entries))
	held := 0
	for _, e := range entries {
		list, err := decodeClientReports(e)
		if err != nil {
			return false, err
		}
		if strings.TrimPrefix(e.Key, reportClientPrefix) == newcomer {
			held = len(list.keys)
			continue
		}
		lists = append(lists, list)
	}
	slices.SortStableFunc(lists, func(a, b clientReports) int { return cmp.Compare(len(b.keys), len(a.keys)) })
	for _, list := range lists {
		if len(list.keys) <= 1 || len(list.keys) <= held {
			break
		}
		for i := len(list.keys) - 1; i >= 0; i-- {
			entry, err := tx.GetKV(ctx, ReportsNamespace, list.keys[i])
			if core.HasCode(err, core.CodeNotFound) {
				continue
			}
			if err != nil {
				return false, err
			}
			var stored storedReport
			if json.Unmarshal(entry.Value, &stored) == nil && !stored.Publish.AttemptedAt.IsZero() {
				continue
			}
			if err := dropReport(ctx, tx, entry); err != nil {
				return false, err
			}
			list.keys = slices.Delete(list.keys, i, i+1)
			if err := list.put(ctx, tx); err != nil {
				return false, err
			}
			count.n = max(count.n-1, 0)
			dropped, err := readCounter(ctx, tx, reportDroppedKey)
			if err != nil {
				return false, err
			}
			dropped.n++
			return true, putCount(ctx, tx, dropped)
		}
	}
	return false, nil
}
