package zptserver

import "github.com/gin-gonic/gin"

func AuthMiddleware(key string) gin.HandlerFunc {

	return func(c *gin.Context) {
		if key != "" {
			reqKey := c.Request.Header.Get("X-Auth-Key")

			if key != reqKey {
				errNotAuthorized(c)
				return
			}
		}
		c.Next()
	}
}