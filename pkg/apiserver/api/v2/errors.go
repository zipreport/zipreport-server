package v2

import (
	"github.com/gin-gonic/gin"
	"net/http"
)

func errBadRequest(c *gin.Context, err error) {
	c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
}

func errServerError(c *gin.Context) {
	c.JSON(http.StatusInternalServerError, gin.H{"error": "unexpected server error"})
}
