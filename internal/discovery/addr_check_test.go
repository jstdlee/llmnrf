package discovery

import (
	"fmt"
	"net/http"
	"testing"
)

func TestAddrValidForNewRequest(t *testing.T) {
	ls, err := Listeners()
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) == 0 {
		t.Skip("no listeners")
	}
	for _, l := range ls {
		url := "http://" + l.Addr() + "/v1/messages"
		if _, err := http.NewRequest("POST", url, nil); err != nil {
			t.Errorf("addr %q (ip=%s port=%d): %v", l.Addr(), l.IP.String(), l.Port, err)
		}
	}
	fmt.Println("checked", len(ls), "listeners")
}
