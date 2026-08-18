---
status: Accepted
date: 2026-08-17
---

# ADR 0001: Contrato universal de compreensão

## Contexto

O `Knowledge Engine` precisa reunir conhecimento de fontes heterogêneas sem
confundir a especialização de cada analisador com compreensão completa ou
uniforme. Analisadores de código, documentos, configurações ou outras fontes
podem alcançar profundidades diferentes, produzir somente algumas dimensões
de compreensão e falhar parcialmente. Ainda assim, suas contribuições devem
poder ser correlacionadas e avaliadas na mesma base de conhecimento viva.

Sem uma linguagem comum, cada analisador imporia um contrato próprio às
experiências e tornaria difícil preservar origem, método, suporte, cobertura,
incerteza e contexto temporal. Um contrato universal também precisa
permanecer conceitual: esta decisão não deve antecipar protocolo, estrutura de
dados, persistência, mecanismo de ingestão ou stack.

## Decisão

Adotamos analisadores especializados projetados para contribuir com um
contrato universal conceitual de compreensão. Cada analisador mantém a
semântica específica da sua `Source`, mas publica resultados que podem ser
correlacionados por conceitos comuns e cuja cobertura parcial permanece
explícita.

O contrato comum orienta dimensões de inventário e estrutura, entidades e
relações, fluxos, decisões, variações configuráveis, capacidades, erros,
evolução, documentação, evidências, proveniência, incerteza e lacunas. Cada
análise declara o escopo tentado e, por dimensão, o que foi produzido, ficou
incompleto, não é suportado, não é aplicável ou falhou. A correlação preserva
a fonte, o método, o contexto e o suporte de cada contribuição; não transforma
uma ausência em conhecimento nem uma inferência em observação.

Esta decisão define significado e fronteiras de colaboração, não um formato
de serialização ou uma implementação. A avaliação deve usar perguntas de
competência e referências revisáveis, em vez de tratar volume de documentação
como prova de compreensão. A IA pode sintetizar, explicar, classificar ou
apoiar consultas quando autorizada, mas não é evidência técnica autossuficiente
nem condição para que resultados determinísticos e curados continuem
disponíveis.

Os qualificadores canônicos permanecem independentes: `Analysis Coverage` e
`Explicit Gap` tornam alcance e ausência visíveis; `Possible Flow`, `Observed Execution`
e `Business Process` distinguem as realidades comportamentais;
`Capability` nomeia algo oferecido pelo ambiente e `Knowledge Product` nomeia
uma composição produzida pelo Manu. Comparações preservam, quando conhecidos,
`Source Revision`, `Analysis Snapshot`, `Environment`, `Release`, `Build
Artifact`, `Deployment`, `Configuration State` e `Documentation Revision`.

## Alternativas consideradas

- **Contratos isolados por analisador:** preservariam a liberdade de cada
  módulo, mas transfeririam a complexidade de integração para as experiências
  e impediriam uma comparação coerente de cobertura entre fontes.
- **Um grafo único como contrato:** facilitaria representar relações, mas não
  expressaria sozinho origem epistemológica, suporte, cobertura parcial,
  temporalidade, conflitos e distinções de comportamento.
- **Pipeline centrado em IA:** aceleraria sínteses e explicações, mas faria
  evidência, previsibilidade, privacidade, custo e disponibilidade dependerem
  do modelo, além de não oferecer uma base suficiente quando a IA estiver
  indisponível ou proibida.

## Trade-offs e consequências

- O contrato comum permite correlação entre fontes e evolução comparável por
  perguntas de competência, preservando extensões especializadas.
- Uma camada universal pode ficar abstrata demais ou exigir vocabulário
  adicional; perguntas e referências reais do MVP devem orientar sua evolução.
- A cobertura parcial fica mais honesta, mas experiências precisam explicar
  diferenças de profundidade sem convertê-las em um selo binário.
- Analisadores avançados terão o custo de projetar suas contribuições nos
  conceitos comuns, sem perder detalhes que só a fonte sustenta.
- A IA permanece opcional e subordinada às evidências; sem ela, explicações
  geradas podem ser limitadas, enquanto conhecimento não dependente de IA
  continua utilizável.
- A decisão preserva liberdade para escolher futuramente protocolo,
  persistência, mecanismos de ingestão e stack quando uma capacidade concreta
  justificar essas escolhas; não garante compatibilidade uniforme entre todas
  as fontes desde o primeiro incremento.

## Relações

- OpenSpec: [proposta arquivada](../../openspec/changes/archive/2026-08-17-define-knowledge-engine-understanding-contract/proposal.md) e [especificação principal da capacidade](../../openspec/specs/knowledge-engine-comprehension/spec.md)
- Documentos afetados: [`ARCHITECTURE.md`](../../ARCHITECTURE.md), [`PRODUCT.md`](../../PRODUCT.md) e [`DOMAIN.md`](../../DOMAIN.md)
- ADR substituído/substituto: não aplicável; este é o primeiro ADR do projeto.
