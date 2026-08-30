---
status: Accepted
date: 2026-08-23
---

# ADR 0005: Kernel factual, frontends substituíveis e intercâmbio versionado

## Contexto

O `Knowledge Engine` precisa reunir contribuições de Java/Quarkus, WSO2 e
Python/Frappe sem fazer com que cada consumidor conheça o payload privado de
um analisador. O corte atual já preserva observações, evidências, cobertura e
lacunas, mas ainda não fixa a fronteira entre contribuição de frontend,
normalização, derivação e recuperação. Sem essa fronteira, uma nova ferramenta
poderia apagar contribuições anteriores, confundir uma inferência com uma
observação ou acoplar o Agent a um parser, indexador ou runtime específico.

O `Manu Agent` deve continuar local, determinístico, sem execução da fonte,
sem rede e compatível com builds Linux estáticos. PostgreSQL continua sendo a
fonte de verdade operacional e suas projeções são reconstruíveis. A
comparação de ferramentas desta mudança está em
[`docs/verification/1-5-frontend-comparison.md`](../verification/1-5-frontend-comparison.md);
ela ainda não constitui uma medição completa do corpus heterogêneo.

## Decisão

Adotamos um kernel factual técnico como fronteira comum do pipeline. A
sequência conceitual é:

```text
contribuição do frontend
  -> normalização sustentada
  -> fatos observados canônicos
  -> fatos derivados com linhagem
  -> projeções e Context Package
```

O kernel deve:

- preservar a identidade do `Organization`, `Source` e `Analysis Snapshot`,
  o predicado, os participantes ou valor, qualificadores, produtor, versão,
  método, evidências e locadores;
- aceitar somente mapeamentos universais sustentados; detalhes sem
  equivalente seguro permanecem em extensões versionadas identificadas;
- combinar contribuições de forma aditiva, mantendo produtores, evidências,
  cobertura, lacunas e conflitos distinguíveis;
- permitir que fatos observados sejam reconstruídos sem serem reclassificados
  como fatos derivados ou gerados.

Frontends são substituíveis. Cada um declara identidade, versão, método,
famílias e versões de fonte reconhecidas, capacidades, limitações e perfil de
execução. O consumidor consulta o contrato comum e não depende da ferramenta
que produziu uma contribuição. Uma família reconhecida não implica cobertura
semântica completa.

Derivação ocorre depois da normalização, por regras versionadas e
determinísticas. Cada fato derivado mantém a regra, a versão e todos os fatos
de entrada que sustentam sua conclusão. Regras monotônicas podem ser
reexecutadas até o ponto fixo ou até um limite controlado; atingir um limite
gera cobertura incompleta e lacuna, não uma relação truncada publicada como
completa. A atualização incremental pode reutilizar fatos compatíveis e
reprocessar o fanout afetado, sempre preservando a possibilidade de comparar
o resultado com um rebuild completo.

O `Analysis Bundle` é a fronteira de intercâmbio versionada. Ele pode carregar
manifestos de frontend, contribuições normalizadas, fatos e extensões
específicas, e deve ser validado por escopo, identidade, produtor, versão,
locadores, limites e schema antes da persistência. Ferramentas externas
produzem um bundle ou seção de intercâmbio validável; não há ABI de plugin Go
obrigatória nem acesso direto de um frontend externo ao banco ou à fonte.

Os perfis de execução são:

- `safe-static`: padrão do Agent; sem rede, build, instalação de dependências
  ou execução da fonte;
- `semantic-isolated`: opção explícita e isolada para uma especialização que
  precise de compilador ou indexador externo;
- `imported-index`: aceita um índice produzido previamente, aplicando as
  mesmas validações de escopo, locador, produtor e limite sem executar a
  ferramenta produtora.

Nenhum dos três candidatos comparados nesta mudança é promovido ao núcleo.
Tree-sitter pode ser avaliado como frontend sintático, SCIP como formato de
intercâmbio e Joern como produtor externo de análise de código, mas cada uso
exige evidência do corpus, locadores verificáveis, determinismo, licença,
isolamento, tamanho operacional e compatibilidade com o build estático. Até
essa evidência existir, o núcleo usa as capacidades locais já verificadas e
mantém adaptadores opcionais fora do binário principal.

O `Context Package` e o MCP são consumidores substituíveis dessa fronteira:
nenhum deles redefine o kernel, acessa a persistência diretamente ou concede
acesso à `Source`. Toda recuperação resolve `Organization`, `Source`,
snapshot, autorização e orçamento antes de expor uma evidência; transferência
para IA continua uma decisão separada do acesso local.

## Alternativas consideradas

- **Ampliar indefinidamente `Contribution.Value`:** preservaria o envelope
  atual, mas faria cada consumidor interpretar formatos privados e impediria
  substituição segura de frontends.
- **Adotar um parser universal obrigatório:** ofereceria uma integração inicial
  uniforme, mas não cobriria framework, configuração, documentos, WSO2/CAR ou
  contexto de negócio com a mesma semântica e acoplaria o Agent a uma
  ferramenta.
- **Usar um CPG ou Joern como contrato do núcleo:** daria relações e análise
  de código, mas não representaria todas as fontes, evidências, curadoria,
  temporalidade e políticas do `Knowledge Engine`; também contrariaria o
  perfil estático atual.
- **Persistir somente relações derivadas ou pedir à LLM que as complete:**
  reduziria a representação imediata, mas perderia linhagem, determinismo,
  reconstrução e a separação entre observado e gerado.
- **Embutir Tree-sitter, SCIP ou Joern antes da medição:** reduziria o tempo
  até uma integração específica, mas promoveria dependências e runtimes sem
  demonstrar cobertura, locadores e operação adequados ao Agent.

## Trade-offs e consequências

- O kernel fornece uma fronteira estável para consumidores e permite evolução
  independente de frontends, ao custo de manter manifestos, extensões e
  validação de intercâmbio.
- A composição aditiva preserva conflito e cobertura parcial, mas exige que
  consumidores saibam lidar com mais de uma contribuição para o mesmo contexto.
- A linhagem torna derivação e invalidação explicáveis e reconstruíveis, ao
  custo de armazenar vínculos e versões de regra.
- O perfil `safe-static` mantém o Agent previsível e pequeno, mas algumas
  especializações só estarão disponíveis como lacuna ou em perfil isolado.
- A decisão não escolhe uma biblioteca, uma linguagem de frontend, um CPG,
  um protocolo MCP ou uma implementação física. Uma dependência opcional só
  poderá entrar em mudança posterior após a avaliação delimitada prevista no
  registro de comparação.

## Relações

- OpenSpec: [proposta](../../openspec/changes/archive/2026-08-30-establish-evidence-backed-context-engine/proposal.md), [design](../../openspec/changes/archive/2026-08-30-establish-evidence-backed-context-engine/design.md) e [especificação do substrato factual](../../openspec/changes/archive/2026-08-30-establish-evidence-backed-context-engine/specs/analysis-fact-substrate/spec.md)
- Documentos afetados: [`PRODUCT.md`](../../PRODUCT.md), [`DOMAIN.md`](../../DOMAIN.md), [`ARCHITECTURE.md`](../../ARCHITECTURE.md) e [registro da comparação](../verification/1-5-frontend-comparison.md)
- ADR substituído/substituto: não aplicável
