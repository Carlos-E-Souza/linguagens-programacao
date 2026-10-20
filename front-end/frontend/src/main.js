import './style.css';
import './app.css';

import logo from './assets/images/logo-universal.png';
import {Saudar} from '../wailsjs/go/main/Aplicacao';

document.querySelector('#app').innerHTML = `
    <img id="logo" class="logo">
      <div class="result" id="resultado">Digite seu nome abaixo 👇</div>
      <div class="input-box" id="entrada">
        <input class="input" id="nome" type="text" autocomplete="off" />
        <button class="btn" onclick="saudar()">Saudar</button>
      </div>
    </div>
`;
document.getElementById('logo').src = logo;

let elementoNome = document.getElementById("nome");
elementoNome.focus();
let elementoResultado = document.getElementById("resultado");

// Configura a função saudar
window.saudar = function () {
    // Obtém o nome
    let nome = elementoNome.value;

    // Verifica se a entrada está vazia
    if (nome === "") return;

    // Chama Aplicacao.Saudar(nome)
    try {
        Saudar(nome)
            .then((resultado) => {
                // Atualiza o resultado com o retorno de Aplicacao.Saudar()
                elementoResultado.innerText = resultado;
            })
            .catch((erro) => {
                console.error(erro);
            });
    } catch (erro) {
        console.error(erro);
    }
};
