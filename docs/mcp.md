# Servidor MCP local

O Manu oferece um adaptador MCP local, somente leitura, para agentes que
precisam consultar o `Context Package` autorizado. O transporte é `stdio`: o
processo recebe mensagens MCP em `stdin` e escreve somente o protocolo MCP em
`stdout`. O protocolo anunciado é `2025-11-25` e a identidade do servidor é
`manu`.

Este adaptador não é uma API HTTP nem um serviço remoto. O comando não abre uma
porta, não oferece autenticação própria e deve ser iniciado pelo cliente MCP no
mesmo ambiente local que possui acesso ao banco configurado.

## Pré-requisitos e inicialização

O MCP usa o `ContextService` produtivo, PostgreSQL e as projeções locais. Antes
da primeira sessão, aplique as migrações com o mesmo ambiente que será usado
pelo servidor:

```bash
export MANU_MCP_ENABLED=true
export MANU_ORGANIZATION_ID=local

manu migrate
manu mcp
```

`manu mcp` bloqueia aguardando o cliente no `stdin`; normalmente o cliente MCP
executa o binário, em vez de um operador digitar mensagens no terminal. O
MCP fica desabilitado por padrão. Sem `MANU_MCP_ENABLED=true`, o comando termina
com código `1` e escreve somente `manu mcp: MCP is disabled` em `stderr`.

`manu migrate` não é executado automaticamente por `manu mcp`. Configure
`MANU_POSTGRES_DSN` ou os campos `MANU_POSTGRES_HOST`,
`MANU_POSTGRES_PORT`, `MANU_POSTGRES_DATABASE`, `MANU_POSTGRES_USER` e,
quando necessário, `MANU_POSTGRES_PASSWORD`, conforme
[`configuration.md`](configuration.md). O banco precisa estar acessível e com
as migrações aplicadas. A organização é única no modo local e vem de
`MANU_ORGANIZATION_ID` (default `local`); cada chamada MCP ainda precisa
declarar essa organização no próprio escopo. Não há enumeração nem acesso entre
organizações.

## Configuração de cliente

Use um caminho absoluto para o executável e habilite o MCP no ambiente do
processo. O formato abaixo é uma configuração genérica de cliente MCP; o
cliente pode usar outro nome de arquivo, mas deve preservar os campos
`command`, `args` e `env`:

```json
{
  "mcpServers": {
    "manu": {
      "command": "/opt/manu/bin/manu",
      "args": ["mcp"],
      "env": {
        "MANU_MCP_ENABLED": "true",
        "MANU_ORGANIZATION_ID": "local",
        "MANU_POSTGRES_HOST": "127.0.0.1",
        "MANU_POSTGRES_PORT": "5432",
        "MANU_POSTGRES_DATABASE": "manu",
        "MANU_POSTGRES_USER": "manu"
      }
    }
  }
}
```

Substitua `/opt/manu/bin/manu` e os valores de conexão pelos do ambiente local.
Quando o banco exigir senha, forneça `MANU_POSTGRES_PASSWORD` por um mecanismo
de segredo ou pelo ambiente do cliente, sem gravá-la em configuração
versionada. A configuração não concede acesso a outra organização nem altera
as políticas do `ContextService`.

## Negociação e capacidades

Após a negociação, o servidor anuncia as ferramentas de leitura e o recurso de
evidência. A implementação atual não anuncia prompts, logging, completions ou
extensões adicionais. A ordem conceitual de registro é:

```text
manu_query
manu_context
manu_impact
manu_evidence
```

O SDK pode devolver a lista de ferramentas em ordem lexicográfica; clientes
devem identificar cada operação pelo campo `name`, não por posição. Todas as
ferramentas são `read-only`, idempotentes e com `open-world=false`.

## Ferramentas

As quatro ferramentas recebem escopo e orçamento explícitos. O schema MCP
filtra `organization_id`, `source_id` e `snapshot_id` como strings não vazias;
na validação da porta, os três valores precisam ser UUIDs canônicos do
`ContextService`, vinculados à mesma organização, fonte e snapshot. O valor
externo `MANU_ORGANIZATION_ID=local`, por exemplo, é convertido para a
identidade UUID interna; não use `local` como `organization_id` de uma chamada.
Reutilize os UUIDs de escopo retornados por ingestão, consulta ou evidência
autorizada. Este corte não oferece ferramenta MCP de descoberta ou enumeração.
Os campos `max_tokens`, `max_items`, `max_characters` e `max_bytes` são
inteiros positivos; zero, negativo, ausente ou extra é rejeitado pelo schema
estrito.
Os limites máximos de representação da porta são, respectivamente,
`1.048.576` (`1048576`), `10.000` (`10000`), `16.777.216` (`16777216`) e
`16.777.216` (`16777216`). O serviço pode aplicar um limite efetivo menor, e o
pacote informa o orçamento usado.

Para leituras pelo recurso `resources/read`, o runtime compõe o orçamento a
partir de `MANU_RETRIEVAL_MAX_PACKAGE_TOKENS`,
`MANU_RETRIEVAL_MAX_PACKAGE_UNITS` e `MANU_RETRIEVAL_MAX_PACKAGE_BYTES`; os
defaults são `16.000` (`16000`), `16` e `262.144` (`262144`) bytes,
respectivamente. Nesse recurso,
`max_characters` também usa o limite configurado de bytes. As ferramentas
recebem o orçamento explicitamente em cada chamada e continuam sujeitas aos
limites máximos da porta e às políticas do serviço.

`continuation` é opcional, string opaca de até 4 KiB. Ela só pode ser reutilizada
com o mesmo escopo, snapshot, intenção, política, algoritmo e ordenação. O
token é assinado com uma chave gerada para o processo e não é persistido; trate-o
como válido apenas na sessão/processo que o emitiu. Ele não amplia autorização.

| Ferramenta | Campos adicionais | Intenção e resultado |
| --- | --- | --- |
| `manu_query` | `question` (string não vazia, até 16 KiB UTF-8) | Consulta por pergunta; retorna contexto limitado e estruturado. |
| `manu_context` | `target_kind` (`entity` ou `symbol`) e `target_id` (ID seguro/opaco, até 256 bytes) | Contexto de entidade ou símbolo autorizado. |
| `manu_impact` | `target_kind` (`entity` ou `symbol`) e `target_id` (ID seguro/opaco, até 256 bytes) | Caminhos e relações de impacto possível; não confirma execução observada. |
| `manu_evidence` | `evidence_id` (ID seguro/opaco, até 256 bytes) | Reinspeção da evidência por identidade, com autorização repetida. |

### Saída estruturada

Cada chamada bem-sucedida retorna um objeto com a forma:

```text
{
  "context": ContextPackage,
  "latest_snapshot_id": "..."   (opcional)
}
```

`context` é validável e contém, entre outros metadados, `version`, `id`,
`digest`, `revision`, `scope`, `intent`, `limits`, `items`, `relations`,
`coverage`, `gaps`, `degradations`, `audit`, `token_estimate`,
`characters_used`, `bytes_used`, `truncated` e, quando necessário,
`continuation`. Conteúdo
protegido não é promovido para o pacote; indisponibilidade ou filtragem aparece
como erro opaco ou degradação controlada.

`latest_snapshot_id` é apenas uma indicação. Quando uma consulta histórica
encontra uma revisão ativa posterior, o campo pode aparecer, mas
`context.scope.snapshot_id` e `context.revision` permanecem históricos. Para o
snapshot atual, a indicação não aparece.

## Recurso de evidência

Itens de evidência retornados podem incluir `ResourceLink` neste namespace:

```text
manu://organizations/{organization}/sources/{source}/snapshots/{snapshot}/evidence/{id}
```

Essa URI é uma identidade de aplicação, nunca um caminho de filesystem. O
cliente pode ler o recurso usando `resources/read`; a leitura é feita de novo
pelo `ContextService`, com escopo, snapshot, identidade e autorização
revalidados. A resposta usa `application/json` e preserva o pacote histórico;
uma revisão ativa mais nova pode ser indicada sem substituir a revisão pedida.
Ao contrário das ferramentas, o JSON de `resources/read` embute o
`ContextPackage` no topo do objeto e acrescenta somente o campo opcional
`latest_snapshot_id`; ele não usa o envelope `{"context": ...}` das tools.
URI malformada, UUID de escopo inválido ou evidência fora do escopo falha de
modo opaco e não revela se o recurso existe.

## Auditoria e erros

`stdout` é reservado ao protocolo MCP. O runtime local escreve auditoria
JSONL content-free em `stderr`, uma linha por ferramenta ou leitura de recurso.
O registro contém apenas versão, operação, escopo, orçamento, resultado,
duração, revisão quando houve sucesso, truncamento e IDs de itens/relações
entregues. Não contém pergunta, alvo textual, conteúdo de evidência,
locator/path, token de continuação, erro interno, credencial ou SQL. O JSONL é
somente um fluxo do processo: não é armazenado de forma durável pelo adaptador.
Uma falha ao registrar a auditoria impede a entrega do sucesso.

Os resultados e erros permanecem limitados: entradas malformadas, orçamento
inválido, cursor incompatível, escopo não autorizado e indisponibilidade usam
schemas, degradações ou sentinelas opacas. O cliente não recebe SQL, Cypher,
stack trace, credencial ou conteúdo negado.

## Limites deste corte

O adaptador é somente leitura e local. Não oferece:

- transporte HTTP ou remoto;
- autenticação de produção ou organização compartilhada;
- acesso direto à `Source`, PostgreSQL, SQL, Cypher ou qualquer consulta livre;
- mutação, administração, rebuild de projeções ou operação de ingestão;
- execução de `Generator` ou geração de texto.

O `ContextService` pode usar fatos, projeções e sinais disponíveis no banco,
sempre dentro da política, do escopo e do orçamento. Se não houver dados
ingeridos, projeção compatível ou suporte suficiente, a resposta permanece
limitada e sinaliza indisponibilidade, lacuna ou degradação controlada.
O runtime não invoca `Generator`. Se `MANU_EMBEDDING_ENABLED=true`, a
recuperação pode usar o `Embedder` opcional; essa chamada continua condicionada
ao provedor, modelo, endpoint, credencial, política de transferência e
orçamento configurados, e não é habilitada por padrão.
