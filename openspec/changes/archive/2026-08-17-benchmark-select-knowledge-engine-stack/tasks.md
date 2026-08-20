## 1. Preparar a fundação e registrar a decisão

- [x] 1.1 Confirmar com o usuário o caminho canônico do módulo, escolher instalação local ou imagem como fonte do toolchain e verificar a versão estável oficial antes de criar `go.mod`.
- [x] 1.2 Criar o ADR 0002 para a decisão Go-first, incluindo política de versões suportadas, alternativas, permissão para analisadores futuros em outros runtimes e relação com esta mudança.
- [x] 1.3 Atualizar o índice de ADRs e a arquitetura canônica com a decisão aceita, sem promover persistência, IA, protocolo externo ou Compose completo a implementação existente.
- [x] 1.4 Inicializar o módulo único, o layout `cmd`/`internal`/`testdata` e a identificação de versão, mantendo o primeiro incremento sem dependências de runtime externas.

## 2. Materializar o contrato operacional e a CLI

- [x] 2.1 Implementar os tipos do resultado `v1alpha1`, estados de cobertura, lacunas, falhas, locadores e metadados de execução, com validação e testes de tabela.
- [x] 2.2 Implementar identidades e ordenação determinísticas para fontes, artefatos e contribuições, com testes que separem equivalência factual de metadados da execução.
- [x] 2.3 Implementar leitura e escrita atômica e em fluxo do manifesto e das sequências JSON, com golden tests e rejeição de versões incompatíveis.
- [x] 2.4 Implementar a raiz da CLI e os comandos `version`, `analyze` e `inspect` com `flag`, saída humana/JSON e códigos de saída para sucesso, parcial, uso inválido e falha técnica.

## 3. Implementar descoberta e leitura segura

- [x] 3.1 Implementar normalização da raiz, inclusões/exclusões, recusa de links e arquivos especiais e hashing SHA-256 em fluxo, com testes de traversal e preservação da fonte.
- [x] 3.2 Implementar limites de arquivos, bytes, duração e concorrência e propagar cancelamento pelo pipeline, com testes de prazo, cancelamento e ausência de vazamento de goroutines.
- [x] 3.3 Implementar abertura de ZIP/CAR sem extração, validando caminhos, quantidade de membros, tamanho expandido, razão de expansão e formatos não suportados, com testes negativos e fuzzing.
- [x] 3.4 Implementar classificação textual/binária, exclusões sensíveis padrão e extração genérica limitada sem emitir conteúdo integral em logs ou resultados por padrão.

## 4. Compor analisadores no pipeline comum

- [x] 4.1 Implementar a interface interna, descritores, registro, seleção e execução limitada de analisadores, preservando fallback e falhas isoladas em testes.
- [x] 4.2 Implementar o analisador genérico de inventário e texto, com `Artifact`, hash, tipo, locador, cobertura e lacunas reproduzíveis.
- [x] 4.3 Implementar o extrator Java inicial para pacotes, imports, tipos, métodos, anotações, configurações, exceções e relações diretas conservadoras, emitindo somente contribuições com locador e cobertura explícita.
- [x] 4.4 Implementar o analisador declarativo WSO2 para membros CAR e XML em fluxo, incluindo tipos, referências literais, imports/includes e lacunas para relações dinâmicas ou não suportadas.
- [x] 4.5 Criar fixtures determinísticas Java, WSO2/CAR, XML, texto, binário e casos hostis e verificar o pipeline integrado sem acessar o corpus externo.

## 5. Implementar reutilização e atualização localizada

- [x] 5.1 Implementar o estado reconstruível com chaves por fonte, artefato, hash, contrato e versão do analisador, gravado somente no destino configurado.
- [x] 5.2 Implementar repetição sem mudança, reutilizando apenas resultados compatíveis e comparando equivalência factual em testes.
- [x] 5.3 Implementar atualização localizada por segunda revisão ou overlay efêmero, invalidando o artefato alterado e dependentes diretos conhecidos sem escrever na fonte.
- [x] 5.4 Expor no resultado e em `inspect` contadores de descobertos, reutilizados, reprocessados, limitados e com falha, além das limitações da estratégia incremental.

## 6. Medir o microcorte e registrar a linha de base

- [x] 6.1 Implementar coleta de duração por etapa, bytes, volume de saída, concorrência, heap Go e pico residente Linux quando disponível, declarando métricas indisponíveis.
- [x] 6.2 Implementar o comando `benchmark` para primeira análise, repetição sem mudança e atualização localizada, com configuração e relatório estruturado reproduzíveis.
- [x] 6.3 Adicionar benchmarks `testing.B` somente para hot paths identificados e documentar o método de repetição/comparação, sem usar resultado sintético isolado como decisão.
- [x] 6.4 Executar de forma explícita e somente leitura o microcorte no Ticketmaster, nos seis CARs e no inventário ERPNext nas revisões/hashes do manifesto, verificando que nenhuma fonte foi alterada.
- [x] 6.5 Registrar uma linha de base concisa em `docs/evaluation/` com ambiente, configuração, resultados, cobertura, limitações e lacunas, sem SLA, conteúdo copiado do corpus ou segredo.
- [x] 6.6 Comparar os resultados com os critérios do protocolo e registrar honestamente qualquer falha que exija ajustar ou substituir a decisão Go-first.

## 7. Empacotar e verificar a distribuição Linux

- [x] 7.1 Criar build multi-stage com toolchain fixado e imagem `scratch` não privilegiada para `linux/amd64` e `linux/arm64`, além de `.dockerignore` mínimo.
- [x] 7.2 Verificar a imagem com fonte montada somente para leitura, saída separada e sem banco, modelo, credencial ou serviço de cloud.
- [x] 7.3 Executar formatação, `go vet`, testes, detector de corrida quando suportado, build das arquiteturas e verificação de vulnerabilidades; corrigir falhas e registrar ferramentas opcionais indisponíveis.
- [x] 7.4 Atualizar `README.md` com pré-requisitos, comandos reais verificados, limites do microcorte e a explicação de que a CLI é o primeiro modo do `Manu Agent`, não um daemon com IA local.

## 8. Revisar coerência e concluir a mudança

- [x] 8.1 Revisar o código contra os requisitos de runtime e de compreensão aplicáveis, garantindo cobertura, evidência, proveniência, lacunas e falhas parciais sem falsa profundidade semântica.
- [x] 8.2 Verificar links relativos, ausência de placeholders e segredos, responsabilidade dos documentos e coerência entre ADR, arquitetura, CLI, corpus e protocolo de avaliação.
- [x] 8.3 Executar `openspec validate benchmark-select-knowledge-engine-stack --strict` e a suíte real documentada, corrigindo todas as violações antes de marcar a mudança como concluída.
