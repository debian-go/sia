package api

import (
	"net/http"

	"github.com/NebulousLabs/Sia/types"

	"github.com/julienschmidt/httprouter"
)

type TransactionPoolGET struct {
	Transactions []types.Transaction `json:"transactions"`
}

// transactionpoolTransactionsHandler handles the API call to get the
// transaction pool trasactions.
func (api *API) transactionpoolTransactionsHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	WriteJSON(w, TransactionPoolGET{Transactions: api.tpool.TransactionList()})
}
