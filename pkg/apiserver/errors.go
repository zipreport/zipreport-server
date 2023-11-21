package apiserver

import "github.com/gin-gonic/gin"

func errNotAuthorized(c *gin.Context) {
	c.AbortWithStatusJSON(401,
		gin.H{
			"error": "Not authorized",
		})
}
