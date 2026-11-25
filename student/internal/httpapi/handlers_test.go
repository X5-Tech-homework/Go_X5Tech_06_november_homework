package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"example.com/lesson9practice/internal/repository"
	"example.com/lesson9practice/internal/service"
)

func newHandler() http.Handler {
	return New(service.New(repository.New()), slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
}

func request(handler http.Handler, method, path, body, contentType string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestCreateAndReadOrder(t *testing.T) {
	handler := newHandler()
	created := request(handler, "POST", "/api/v1/orders", `{"customer":"  Алиса  "}`, "application/json")
	if created.Code != 201 {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body)
	}
	if got := created.Header().Get("Location"); got != "/api/v1/orders/1" {
		t.Fatalf("Location=%q", got)
	}
	var order OrderResponse
	if err := json.Unmarshal(created.Body.Bytes(), &order); err != nil {
		t.Fatal(err)
	}
	if order.ID != 1 || order.Customer != "Алиса" || order.Status != "new" {
		t.Fatalf("unexpected order: %+v", order)
	}
	read := request(handler, "GET", created.Header().Get("Location"), "", "")
	if read.Code != 200 || read.Body.String() != created.Body.String() {
		t.Fatalf("read status=%d body=%s", read.Code, read.Body)
	}
	// Повторный заказ одного клиента разрешён: имя не является уникальным ключом.
	again := request(handler, "POST", "/api/v1/orders", `{"customer":"Алиса"}`, "application/json")
	if again.Code != 201 || again.Header().Get("Location") != "/api/v1/orders/2" {
		t.Fatalf("second create status=%d Location=%q", again.Code, again.Header().Get("Location"))
	}
}

func TestErrorsKeepJSONContract(t *testing.T) {
	tests := []struct {
		name, method, path, body, contentType string
		status                                int
		code                                  string
	}{
		{"invalid id", "GET", "/api/v1/orders/abc", "", "", 400, "INVALID_ID"},
		{"zero id", "GET", "/api/v1/orders/0", "", "", 400, "INVALID_ID"},
		{"missing order", "GET", "/api/v1/orders/123", "", "", 404, "ORDER_NOT_FOUND"},
		{"missing route", "GET", "/api/v1/missing", "", "", 404, "ROUTE_NOT_FOUND"},
		{"wrong method", "PUT", "/api/v1/orders/1", "", "", 405, "METHOD_NOT_ALLOWED"},
		{"empty customer", "POST", "/api/v1/orders", `{"customer":" "}`, "application/json", 400, "VALIDATION_ERROR"},
		{"null body", "POST", "/api/v1/orders", `null`, "application/json", 400, "INVALID_JSON"},
		{"wrong type", "POST", "/api/v1/orders", `{"customer":42}`, "application/json", 400, "INVALID_JSON"},
		{"unknown field", "POST", "/api/v1/orders", `{"customer":"Алиса","status":"paid"}`, "application/json", 400, "INVALID_JSON"},
		{"second JSON", "POST", "/api/v1/orders", `{"customer":"Алиса"} {}`, "application/json", 400, "INVALID_JSON"},
		{"wrong media type", "POST", "/api/v1/orders", `{"customer":"Алиса"}`, "text/plain", 415, "UNSUPPORTED_MEDIA_TYPE"},
		{"large body", "POST", "/api/v1/orders", `{"customer":"` + strings.Repeat("a", 1100) + `"}`, "application/json", 413, "BODY_TOO_LARGE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := request(newHandler(), tt.method, tt.path, tt.body, tt.contentType)
			if w.Code != tt.status {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tt.status, w.Body)
			}
			if got := w.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type=%q", got)
			}
			var out ErrorResponse
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if out.Code != tt.code || out.Error == "" {
				t.Fatalf("unexpected error: %+v", out)
			}
		})
	}
}

func TestUnsupportedMethodDescribesAllowedMethods(t *testing.T) {
	handler := newHandler()
	for _, tt := range []struct{ method, path, allow string }{
		{"DELETE", "/api/v1/orders/1", "GET, HEAD"},
		{"POST", "/api/v1/status", "GET, HEAD"},
	} {
		w := request(handler, tt.method, tt.path, "", "")
		if w.Code != 405 || w.Header().Get("Allow") != tt.allow {
			t.Errorf("%s %s: status=%d Allow=%q", tt.method, tt.path, w.Code, w.Header().Get("Allow"))
		}
	}
}

func TestStatus(t *testing.T) {
	w := request(newHandler(), "GET", "/api/v1/status", "", "")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"status":"ok"}` {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
}

func TestListOrdersPagination(t *testing.T) {
	handler := newHandler()

	// Пустая выборка должна быть JSON-массивом, а не null.
	empty := request(handler, "GET", "/api/v1/orders", "", "")
	if empty.Code != http.StatusOK || strings.TrimSpace(empty.Body.String()) != "[]" {
		t.Fatalf("empty list: status=%d body=%s", empty.Code, empty.Body)
	}

	// Создаём три заказа с последовательными ID.
	for _, customer := range []string{"Алиса", "Борис", "Вера"} {
		created := request(
			handler,
			"POST",
			"/api/v1/orders",
			`{"customer":"`+customer+`"}`,
			"application/json",
		)
		if created.Code != http.StatusCreated {
			t.Fatalf("create order: status=%d body=%s", created.Code, created.Body)
		}
	}

	// Пропускаем первый заказ и получаем только второй.
	page := request(handler, "GET", "/api/v1/orders?limit=1&offset=1", "", "")
	if page.Code != http.StatusOK {
		t.Fatalf("page: status=%d body=%s", page.Code, page.Body)
	}

	var orders []OrderResponse
	if err := json.Unmarshal(page.Body.Bytes(), &orders); err != nil {
		t.Fatal(err)
	}
	if len(orders) != 1 || orders[0].ID != 2 || orders[0].Customer != "Борис" {
		t.Fatalf("unexpected page: %+v", orders)
	}

	// Максимальное значение int допустимо как offset и должно дать пустую страницу.
	maxInt := int(^uint(0) >> 1)
	path := "/api/v1/orders?offset=" + strconv.Itoa(maxInt)
	largeOffset := request(handler, "GET", path, "", "")
	if largeOffset.Code != http.StatusOK || strings.TrimSpace(largeOffset.Body.String()) != "[]" {
		t.Fatalf("large offset: status=%d body=%s", largeOffset.Code, largeOffset.Body)
	}
}

func TestListOrdersRejectsInvalidQuery(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{"empty limit", "/api/v1/orders?limit="},
		{"non-numeric limit", "/api/v1/orders?limit=abc"},
		{"signed limit", "/api/v1/orders?limit=+2"},
		{"zero limit", "/api/v1/orders?limit=0"},
		{"limit too large", "/api/v1/orders?limit=101"},
		{"negative offset", "/api/v1/orders?offset=-1"},
		{"overflow offset", "/api/v1/orders?offset=999999999999999999999999999999999999"},
		{"duplicate limit", "/api/v1/orders?limit=5&limit=6"},
		{"unknown parameter", "/api/v1/orders?page=1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := request(newHandler(), "GET", tt.path, "", "")
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusBadRequest, w.Body)
			}

			var got ErrorResponse
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Code != "INVALID_QUERY" {
				t.Fatalf("error code=%q want=INVALID_QUERY", got.Code)
			}
		})
	}
}

func TestCancelOrder(t *testing.T) {
	handler := newHandler()

	created := request(handler, "POST", "/api/v1/orders",
		`{"customer":"Алиса"}`, "application/json")
	if created.Code != http.StatusCreated {
		t.Fatalf("create: status=%d body=%s", created.Code, created.Body)
	}

	cancelPath := "/api/v1/orders/1/status"
	body := `{"status":"cancelled"}`

	first := request(handler, "PATCH", cancelPath, body, "application/json")
	if first.Code != http.StatusOK {
		t.Fatalf("first cancel: status=%d body=%s", first.Code, first.Body)
	}
	var order OrderResponse
	if err := json.Unmarshal(first.Body.Bytes(), &order); err != nil {
		t.Fatal(err)
	}
	if order.ID != 1 || order.Status != "cancelled" {
		t.Fatalf("unexpected cancelled order: %+v", order)
	}

	again := request(handler, "PATCH", cancelPath, body, "application/json")
	if again.Code != http.StatusConflict {
		t.Fatalf("repeat cancel: status=%d body=%s", again.Code, again.Body)
	}
	var conflict ErrorResponse
	if err := json.Unmarshal(again.Body.Bytes(), &conflict); err != nil {
		t.Fatal(err)
	}
	if conflict.Code != "ORDER_ALREADY_CANCELLED" {
		t.Fatalf("conflict code=%q", conflict.Code)
	}

	missing := request(handler, "PATCH", "/api/v1/orders/999/status",
		body, "application/json")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing order: status=%d body=%s", missing.Code, missing.Body)
	}

	invalid := request(handler, "PATCH", "/api/v1/orders/1/status",
		`{"status":"new"}`, "application/json")
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status: status=%d body=%s", invalid.Code, invalid.Body)
	}
}

func TestConcurrentCancelOrder(t *testing.T) {
	handler := newHandler()

	created := request(handler, "POST", "/api/v1/orders",
		`{"customer":"Борис"}`, "application/json")
	if created.Code != http.StatusCreated {
		t.Fatalf("create: status=%d body=%s", created.Code, created.Body)
	}

	const requests = 2
	statuses := make(chan int, requests)

	for range requests {
		go func() {
			w := request(handler, "PATCH", "/api/v1/orders/1/status",
				`{"status":"cancelled"}`, "application/json")
			statuses <- w.Code
		}()
	}

	successes, conflicts := 0, 0
	for range requests {
		switch status := <-statuses; status {
		case http.StatusOK:
			successes++
		case http.StatusConflict:
			conflicts++
		default:
			t.Errorf("unexpected concurrent response status: %d", status)
		}
	}

	if successes != 1 || conflicts != 1 {
		t.Fatalf("got %d successes and %d conflicts; want 1 each", successes, conflicts)
	}
}
