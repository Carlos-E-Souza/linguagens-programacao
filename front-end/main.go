package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var arquivos embed.FS

func main() {
	// Cria uma instância da estrutura da aplicação
	aplicacao := NovaAplicacao()

	// Cria a aplicação com as opções
	err := wails.Run(&options.App{
		Title:  "chat-client",
		Width:  1024,
		Height: 768,
		AssetServer: &assetserver.Options{
			Assets: arquivos,
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		OnStartup:        aplicacao.iniciar,
		Bind: []interface{}{
			aplicacao,
		},
	})

	if err != nil {
		println("Erro:", err.Error())
	}
}
