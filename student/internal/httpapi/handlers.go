package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"example.com/lesson9practice/internal/model"
	"example.com/lesson9practice/internal/repository"
	"example.com/lesson9practice/internal/service"
)

type Orders interface {
	Create(string) (model.Order, error)
	ByID(int) (model.Order, error)
	List(limit, offset int) []model.Order
	Cancel(id int) (model.Order, error)
}
type API struct {
	orders Orders
	logger *slog.Logger
}

func New(orders Orders, logger *slog.Logger) *API { return &API{orders: orders, logger: logger} }

// ststus godoc
// @Summary     Проверка статуса API.
// @Description Возвращает статус 200 к API.
// @Tags        status
// @Produce     json
// @Success     200 {object} StatusResponse
// @Router      /status [get]
func (a *API) status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, StatusResponse{Status: "ok"})
}

// createOrder godoc
// @Summary     Создание нового заказа.
// @Description Создаёт новый заказ и возвращает его ID.
// @Tags        orders
// @Accept      json
// @Produce     json
// @Param       order body CreateOrderRequest true "Данные заказа"
// @Success     201 {object} OrderResponse
// @Header      201 {string} Location "/api/v1/orders/{id}"
// @Failure     400 {object} ErrorResponse
// @Failure     500 {object} ErrorResponse
// @Router      /orders [post]
func (a *API) createOrder(w http.ResponseWriter, r *http.Request) {
	// Указатель позволяет отличить null от объекта. Теги DTO сами не валидируют запрос.
	var in *CreateOrderRequest
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, err.status, err.message, err.code)
		return
	}
	if in == nil {
		writeError(w, 400, "body must be a JSON object", "INVALID_JSON")
		return
	}
	order, err := a.orders.Create(in.Customer)
	if errors.Is(err, service.ErrEmptyCustomer) {
		writeError(w, 400, "customer is required", "VALIDATION_ERROR")
		return
	}
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/orders/"+strconv.Itoa(order.ID))
	writeJSON(w, 201, orderResponse(order))
}

// getOrder godoc
// @Summary     Получение заказа по ID.
// @Description Возвращает заказ по ID.
// @Tags        orders
// @Produce     json
// @Param       id path int true "ID заказа" minimum(1)
// @Success     200 {object} OrderResponse
// @Failure     400 {object} ErrorResponse
// @Failure     404 {object} ErrorResponse
// @Failure     500 {object} ErrorResponse
// @Router      /orders/{id} [get]
func (a *API) getOrder(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		writeError(w, 400, "id must be a positive integer", "INVALID_ID")
		return
	}
	order, err := a.orders.ByID(id)
	if errors.Is(err, repository.ErrNotFound) {
		writeError(w, 404, "order not found", "ORDER_NOT_FOUND")
		return
	}
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, 200, orderResponse(order))
}

// listOrders godoc
// @Summary     Получение списка заказов.
// @Description Возвращает список заказов с пагинацией.
// @Tags        orders
// @Produce     json
// @Param       limit query int false "Максимум заказов на странице" minimum(1) maximum(100) default(20)
// @Param       offset query int false "Смещение для пагинации" minimum(0) default(0)
// @Success     200 {array} OrderResponse
// @Failure     400 {object} ErrorResponse
// @Failure     500 {object} ErrorResponse
// @Router      /orders [get]
func (a *API) listOrders(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := parsePagination(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid query parameters", "INVALID_QUERY")
		return
	}

	orders := a.orders.List(limit, offset)
	response := make([]OrderResponse, 0, len(orders))
	for _, order := range orders {
		response = append(response, orderResponse(order))
	}

	writeJSON(w, http.StatusOK, response)
}

func parsePagination(r *http.Request) (limit, offset int, ok bool) {
	const defaultLimit = 20

	limit = defaultLimit

	values := r.URL.Query()

	for key, items := range values {
		if key != "limit" && key != "offset" {
			return 0, 0, false
		}
		if len(items) != 1 {
			return 0, 0, false
		}
	}

	parseDigits := func(value string) (int, bool) {
		if value == "" {
			return 0, false
		}
		for i := 0; i < len(value); i++ {
			if value[i] < '0' || value[i] > '9' {
				return 0, false
			}
		}
		n, err := strconv.Atoi(value)
		return n, err == nil
	}

	if items, exists := values["limit"]; exists {
		n, valid := parseDigits(items[0])
		if !valid || n < 1 || n > 100 {
			return 0, 0, false
		}
		limit = n
	}

	if items, exists := values["offset"]; exists {
		n, valid := parseDigits(items[0])
		if !valid {
			return 0, 0, false
		}
		offset = n
	}

	return limit, offset, true
}

// cancelOrder godoc
// @Summary     Отменить заказ
// @Description Переводит заказ из статуса new в cancelled.
// @Tags        orders
// @Accept      json
// @Produce     json
// @Param       id path int true "ID заказа" minimum(1)
// @Param       body body CancelOrderRequest true "Новый статус заказа"
// @Success     200 {object} OrderResponse
// @Failure     400 {object} ErrorResponse
// @Failure     404 {object} ErrorResponse
// @Failure     409 {object} ErrorResponse
// @Failure     500 {object} ErrorResponse
// @Router      /orders/{id}/status [patch]
func (a *API) cancelOrder(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "id must be a positive integer", "INVALID_ID")
		return
	}

	var in *CancelOrderRequest
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, err.status, err.message, err.code)
		return
	}
	if in == nil || in.Status != "cancelled" {
		writeError(w, http.StatusBadRequest, "status must be cancelled", "INVALID_STATUS")
		return
	}

	order, err := a.orders.Cancel(id)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		writeError(w, http.StatusNotFound, "order not found", "ORDER_NOT_FOUND")
	case errors.Is(err, repository.ErrAlreadyCancelled):
		writeError(w, http.StatusConflict, "order already cancelled", "ORDER_ALREADY_CANCELLED")
	case err != nil:
		a.internalError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, orderResponse(order))
	}
}

func (a *API) internalError(w http.ResponseWriter, r *http.Request, err error) {
	a.logger.ErrorContext(r.Context(), "order operation failed", "error", err)
	writeError(w, 500, "internal server error", "INTERNAL_ERROR")
}
