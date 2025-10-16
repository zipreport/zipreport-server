package apiserver

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func errBadRequest(c *gin.Context, errorMessage string) {
	c.JSON(http.StatusBadRequest, gin.H{"error": errorMessage})
}

func errServerError(c *gin.Context) {
	c.JSON(http.StatusInternalServerError, gin.H{"error": "unexpected server error"})
}
