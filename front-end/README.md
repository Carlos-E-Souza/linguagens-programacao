# README

## Sobre

Este é o template oficial Vanilla do Wails.

O projeto pode ser configurado editando o arquivo `wails.json`. Mais informações sobre as configurações do projeto
estão em: https://wails.io/docs/reference/project-config

## Desenvolvimento ao vivo

Para executar em modo de desenvolvimento, rode `wails dev` no diretório do projeto. Isso inicia um
servidor de desenvolvimento do Vite com hot reload muito rápido das alterações no frontend. Para desenvolver no
navegador com acesso aos métodos Go, também há um servidor de desenvolvimento em http://localhost:34115. Conecte-se
a ele pelo navegador e chame o código Go pelas devtools.

A tag `webkit2_41`, necessária em distribuições que só oferecem o WebKitGTK 4.1 (como o Ubuntu 24.04+), já está configurada em `build:tags` no `wails.json`.

## Build

Para gerar um pacote redistribuível em modo de produção, use `wails build`.
