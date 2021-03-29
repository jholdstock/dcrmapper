package server

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// TODO cache info used by gui.

const (
	appName = "Decred Mapper"
)

func getTheme(c *gin.Context) string {
	theme := c.Query("theme")
	if theme != "" {
		c.SetCookie("theme", theme, 3600, "/", "localhost", false, false)
		return theme
	}

	theme, _ = c.Cookie("theme")
	return theme
}

func homepage(c *gin.Context) {
	c.HTML(http.StatusOK, "worldmap.html", gin.H{
		"ActivePage": "WorldMap",
		"Summary":    amgr.GetSummary(),
		"AppName":    appName,
		"Theme":      getTheme(c),
	})
}

func worldNodes(c *gin.Context) {
	// TODO Dont marshal and send all node data, just lat/lon.
	allGoodNodes := amgr.AllGoodNodes()
	c.JSON(http.StatusOK, allGoodNodes)
}

func userAgents(c *gin.Context) {
	c.HTML(http.StatusOK, "user_agents.html", gin.H{
		"ActivePage": "UserAgents",
		"Summary":    amgr.GetSummary(),
		"AppName":    appName,
		"Theme":      getTheme(c),
	})
}

func list(c *gin.Context) {
	count, nodes := amgr.PageOfNodes(0, 10)
	c.HTML(http.StatusOK, "list.html", gin.H{
		"ActivePage": "AllNodes",
		"Node":       nodes,
		"GoodCount":  count,
		"Summary":    amgr.GetSummary(),
		"AppName":    appName,
		"Theme":      getTheme(c),
	})
}

func node(c *gin.Context) {
	ip := c.Query("ip")
	node, good, ok := amgr.GetNode(ip)
	c.HTML(http.StatusOK, "node.html", gin.H{
		"ActivePage": "",
		"Good":       good,
		"Node":       node,
		"OK":         ok,
		"Summary":    amgr.GetSummary(),
		"AppName":    appName,
		"Theme":      getTheme(c),
		"SearchIP":   ip,
	})
}
