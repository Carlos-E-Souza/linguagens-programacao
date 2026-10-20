# Diretório de build

O diretório build guarda todos os arquivos e recursos de build da aplicação.

A estrutura é:

* bin - Diretório de saída
* darwin - Arquivos específicos do macOS
* windows - Arquivos específicos do Windows

## Mac

O diretório `darwin` contém os arquivos específicos dos builds para Mac.
Eles podem ser personalizados e usados no build. Para restaurá-los ao estado padrão, basta apagá-los e
executar `wails build`.

O diretório contém os seguintes arquivos:

- `Info.plist` - o arquivo plist principal usado nos builds para Mac. É usado ao executar `wails build`.
- `Info.dev.plist` - igual ao plist principal, mas usado ao executar `wails dev`.

## Windows

O diretório `windows` contém os arquivos de manifesto e rc usados ao executar `wails build`.
Eles podem ser personalizados para a aplicação. Para restaurá-los ao estado padrão, basta apagá-los e
executar `wails build`.

- `icon.ico` - O ícone da aplicação, usado ao executar `wails build`. Para usar outro ícone, basta substituir
  este arquivo. Se ele não existir, um novo `icon.ico` será criado a partir do arquivo `appicon.png` do
  diretório build.
- `installer/*` - Os arquivos usados para criar o instalador do Windows. São usados ao executar `wails build`.
- `info.json` - Detalhes da aplicação usados nos builds para Windows. Esses dados são usados pelo instalador do
  Windows e pela própria aplicação (clique com o botão direito no exe -> propriedades -> detalhes).
- `wails.exe.manifest` - O arquivo de manifesto principal da aplicação.
