package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

func getPaginationParams(r *http.Request) (first, last int, err error) {
	pageNumber, err := strconv.Atoi(r.FormValue("pageNumber"))
	if err != nil {
		return 0, 0, err
	}
	pageSize, err := strconv.Atoi(r.FormValue("pageSize"))
	if err != nil {
		return 0, 0, err
	}

	if pageNumber < 1 || pageSize < 1 {
		return 0, 0, errors.New("invalid number given for pagenumber or pagesize")
	}

	first = (pageNumber - 1) * pageSize
	last = first + pageSize

	return first, last, nil
}

func paginatedNodes(c *gin.Context) {
	first, last, err := getPaginationParams(c.Request)
	if err != nil {
		fmt.Println(err)
		c.Status(http.StatusBadRequest)
		return
	}

	count, nodes := amgr.PageOfNodes(first, last)
	if err != nil {
		fmt.Println(err)
		c.Status(http.StatusBadRequest)
		return
	}

	c.JSON(http.StatusOK, paginationPayload{
		Count: count,
		Data:  nodes,
	})
}

type paginationPayload struct {
	Data  interface{} `json:"data"`
	Count int         `json:"count"`
}
