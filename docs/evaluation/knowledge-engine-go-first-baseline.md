# Linha de base Go-first do microcorte

**Snapshot:** 17/08/2026, execução local somente leitura, rodada r4.  Esta linha de base
avalia o runtime Go-first do primeiro corte; não é uma comparação completa de
stacks, capacidade comercial ou SLA.

O escopo é o ciclo real do runtime: descoberta/hash, análise, escrita do estado
reconstruível, escrita do resultado, repetição sem mudança e uma atualização
localizada. Persistência vetorial, consulta híbrida, IA, custo e tokens
permanecem fora deste incremento.

## Ambiente e método

- Sistema: `linux/amd64`, CPU `AMD Ryzen 9 5900X 12-Core Processor`,
  `GOMAXPROCS=24`, `NumCPU=24`.
- Toolchain: `go1.26.6`; módulo `github.com/pedrogpaulino/manu`.
- Não houve rede, chamada à OpenAI, execução de conteúdo do corpus ou escrita
  nas fontes. Relatórios e staging ficaram em `/tmp`; nenhum conteúdo foi
  copiado para este repositório.
- A concorrência reportada é o máximo simultâneo observado no pool de jobs de
  análise, com contador atômico; neste recorte o limite configurado foi 4.
- `bytes_read` é o fluxo de hashing/classificação da descoberta. Não inclui
  previews adicionais dos analisadores.
- `persisted_volume_bytes` é o tamanho lógico dos arquivos de resultado e
  estado depois da escrita, não uma medição de syscalls ou bytes físicos de I/O.

Limites explícitos usados nos três comandos: `max-files=10000`,
`max-bytes=1073741824`, `max-file-bytes=268435456`, `max-concurrency=4`,
`max-probe-bytes=8192`, `max-extraction-bytes=1048576`,
`max-archive-members=10000`, `max-archive-bytes=1073741824`,
`max-archive-member-bytes=268435456`,
`max-archive-compressed-bytes=1073741824` e `max-expansion-ratio=1000`.

Os comandos concretos reproduzíveis foram:

```text
# Ticketmaster
env GOCACHE=/tmp/manu-go-cache go run ./cmd/manu benchmark \
  --root /home/pedro_paulino/projetos/doc/system-design-interview-ticketmasters \
  --output /tmp/manu-benchmark-external-20260817-r4/ticketmaster \
  --source-id ticketmaster-java-quarkus \
  --revision 88cab04c59c58e745a94302e5c9e856830c4c902 \
  --include 'app/src/main/java/**' --include 'app/src/main/resources/**' \
  --include 'app/src/test/**' --include 'app/pom.xml' \
  --update-path app/src/main/java/tech/buildrun/controller/BookingController.java \
  --max-files 10000 --max-bytes 1073741824 \
  --max-file-bytes 268435456 --max-concurrency 4 \
  --max-probe-bytes 8192 --max-extraction-bytes 1048576 \
  --max-archive-members 10000 --max-archive-bytes 1073741824 \
  --max-archive-member-bytes 268435456 \
  --max-archive-compressed-bytes 1073741824 \
  --max-expansion-ratio 1000 --json

# WSO2: staging físico com apenas os seis CARs do manifesto
env GOCACHE=/tmp/manu-go-cache go run ./cmd/manu benchmark \
  --root /tmp/manu-benchmark-wso2-staging-20260817-r4 \
  --output /tmp/manu-benchmark-external-20260817-r4/wso2 \
  --source-id wso2-car-sample \
  --revision 23eca6b8f6efdb9e8e671678953c983d6f911d614ca539f5d397c545452a3943 \
  --include ERPProxyServiceCompositeApplication_1.0.0.car \
  --include FIESCArchitectureConfigApplication_1.0.0.car \
  --include FIESCArchitectureRegistryApplication_1.0.0.car \
  --include DocumentosIntegradosDSSApplication_1.0.0.car \
  --include EcommerceSESICompositeApplication_1.0.0.car \
  --include NotaFiscalCompositeApplication_2.0.0.car \
  --update-path NotaFiscalCompositeApplication_2.0.0.car \
  --max-files 10000 --max-bytes 1073741824 \
  --max-file-bytes 268435456 --max-concurrency 4 \
  --max-probe-bytes 8192 --max-extraction-bytes 1048576 \
  --max-archive-members 10000 --max-archive-bytes 1073741824 \
  --max-archive-member-bytes 268435456 \
  --max-archive-compressed-bytes 1073741824 \
  --max-expansion-ratio 1000 --json

# ERPNext
env GOCACHE=/tmp/manu-go-cache go run ./cmd/manu benchmark \
  --root /home/pedro_paulino/projetos/doc/erpnext \
  --output /tmp/manu-benchmark-external-20260817-r4/erpnext \
  --source-id erpnext-inventory \
  --revision 1f839061899c019b9a326b960fc5d10b4b34c761 \
  --include 'erpnext/**' \
  --update-path erpnext/selling/doctype/sales_order/mapper.py \
  --max-files 10000 --max-bytes 1073741824 \
  --max-file-bytes 268435456 --max-concurrency 4 \
  --max-probe-bytes 8192 --max-extraction-bytes 1048576 \
  --max-archive-members 10000 --max-archive-bytes 1073741824 \
  --max-archive-member-bytes 268435456 \
  --max-archive-compressed-bytes 1073741824 \
  --max-expansion-ratio 1000 --json
```

Cada saída foi nova/vazia. A CLI retorna 3 para um relatório parcial; quando
invocada por `go run`, o wrapper do `go` apresenta `exit status 3` como seu
próprio status não zero, enquanto o JSON preserva `partial=true`.

## Corpus, autorização e integridade

As inclusões, exclusões e hashes canônicos estão no
[manifesto do primeiro corte](first-vertical-slice-corpus.md). As verificações
pre/post foram feitas novamente nesta execução:

| Recorte | Identidade verificada | Metadados pre/post |
| --- | --- | --- |
| Ticketmaster | `/home/pedro_paulino/projetos/doc/system-design-interview-ticketmasters`, `HEAD=88cab04c59c58e745a94302e5c9e856830c4c902`; árvores `app`, `app/src`, `app/src/main/java`, `app/src/main/resources` e `app/src/test` coincidiram com o manifesto | estado Git limpo; digest de metadados `288aa3c3…4865a3` antes e depois; `unchanged=true` |
| WSO2/CarbonApps | origem `/home/pedro_paulino/projetos/doc/carbonapps`; diretório original com 132 CARs; hash do manifesto `23eca6b8…2a3943`; os seis nomes e SHA-256 individuais coincidiram com a tabela canônica | hashes dos seis CARs originais iguais pre/post; staging físico em `/tmp/manu-benchmark-wso2-staging-20260817-r4` continha exatamente seis arquivos regulares e nenhum symlink; digest do staging `fb50d231…e145` antes e depois |
| ERPNext | `/home/pedro_paulino/projetos/doc/erpnext`, `HEAD=1f839061899c019b9a326b960fc5d10b4b34c761`; árvore `erpnext=1061f78c…14fcb` e `erpnext/modules.txt=b8b12e…3d244` | estado Git limpo; digest de metadados `4fe436da…97254` antes e depois; `unchanged=true` |

No WSO2, somente os seis CARs exatos foram copiados para o staging temporário
antes da execução; o runtime não recebeu os outros 126 arquivos. O
`update-path` foi, respectivamente, `BookingController.java`,
`NotaFiscalCompositeApplication_2.0.0.car` e
`erpnext/selling/doctype/sales_order/mapper.py`; cada caminho pertenceu aos
artefatos da primeira análise.

A preparação do staging foi uma cópia física explícita, sem symlink:

```text
mkdir -p /tmp/manu-benchmark-wso2-staging-20260817-r4
cp -- \
  /home/pedro_paulino/projetos/doc/carbonapps/ERPProxyServiceCompositeApplication_1.0.0.car \
  /home/pedro_paulino/projetos/doc/carbonapps/FIESCArchitectureConfigApplication_1.0.0.car \
  /home/pedro_paulino/projetos/doc/carbonapps/FIESCArchitectureRegistryApplication_1.0.0.car \
  /home/pedro_paulino/projetos/doc/carbonapps/DocumentosIntegradosDSSApplication_1.0.0.car \
  /home/pedro_paulino/projetos/doc/carbonapps/EcommerceSESICompositeApplication_1.0.0.car \
  /home/pedro_paulino/projetos/doc/carbonapps/NotaFiscalCompositeApplication_2.0.0.car \
  /tmp/manu-benchmark-wso2-staging-20260817-r4/
```

`find` confirmou seis arquivos regulares e zero links; `sha256sum` confirmou
os seis hashes do manifesto antes e depois da execução.

## Resultados estruturados

As durações abaixo estão em milissegundos, convertidas dos nanossegundos
inteiros do JSON: descoberta / análise / escrita de estado / escrita do
resultado / total. `D/R/P` significa descobertos / reutilizados / reprocessados.

| Corpus | Cenário | D/R/P | Duração: descoberta / análise / estado / resultado / total (ms) |
| --- | --- | ---: | ---: |
| Ticketmaster | primeira análise | 70/0/70 | 12,242 / 32,745 / 29,528 / 16,333 / 96,456 |
| Ticketmaster | repetição sem mudança | 70/70/0 | 11,331 / 6,714 / 24,712 / 17,571 / 85,787 |
| Ticketmaster | atualização localizada | 70/69/1 | 11,288 / 6,850 / 25,795 / 15,654 / 86,679 |
| WSO2 seis CARs | primeira análise | 6/0/6 | 0,475 / 26,277 / 16,223 / 13,611 / 59,908 |
| WSO2 seis CARs | repetição sem mudança | 6/6/0 | 0,454 / 0,368 / 14,494 / 12,861 / 44,443 |
| WSO2 seis CARs | atualização localizada | 6/5/1 | 0,411 / 23,128 / 14,788 / 11,608 / 63,347 |
| ERPNext | primeira análise | 5316/0/5316 | 402,972 / 258,967 / 348,164 / 154,232 / 1.240,955 |
| ERPNext | repetição sem mudança | 5316/5316/0 | 272,808 / 199,461 / 352,935 / 152,243 / 1.329,995 |
| ERPNext | atualização localizada | 5316/5315/1 | 269,073 / 199,524 / 392,145 / 161,040 / 1.399,508 |

| Corpus | Cenário | `bytes_read` | volume persistido / saída | concorrência |
| --- | --- | ---: | ---: | ---: |
| Ticketmaster | primeira / repetição / atualização | 79.394 / 79.394 / 79.427 | 2.486.383 / 1.031.908; 2.486.240 / 1.031.905; 2.486.233 / 1.031.896 | 4 / 4 / 4 |
| WSO2 seis CARs | primeira / repetição / atualização | 75.115 / 75.115 / 75.149 | 1.342.361 / 582.507; 1.342.345 / 582.503; 1.342.363 / 582.519 | 4 / 3 / 4 |
| ERPNext | primeira / repetição / atualização | 142.119.855 / 142.119.855 / 142.119.888 | 36.906.302 / 15.148.068; 36.895.666 / 15.148.064; 36.905.690 / 15.148.086 | 4 / 4 / 4 |

Nos três recortes, `repeat_equivalent_facts=true`, `failures=0` e
`limited=0`. A atualização localizada reprocessou exatamente um artefato e
reutilizou os demais artefatos descobertos. Os relatórios ficaram parciais
porque o resultado comum registra lacunas/dimensões sem suporte; isso não foi
uma falha técnica total.

### Memória

Os pares abaixo são `heap_go.heap_alloc_bytes / max_rss_bytes`, em bytes, ao
fim de cada cenário:

| Corpus | Primeira análise | Repetição | Atualização |
| --- | ---: | ---: | ---: |
| Ticketmaster | 6.689.104 / 22.278.144 | 12.247.744 / 26.177.536 | 14.656.200 / 26.062.848 |
| WSO2 seis CARs | 3.272.664 / 15.462.400 | 5.441.848 / 15.736.832 | 4.868.024 / 17.760.256 |
| ERPNext | 42.972.888 / 173.363.200 | 204.573.056 / 271.564.800 | 70.962.200 / 270.774.272 |

`heap_alloc_bytes` é uma amostra final do heap Go. `max_rss_bytes` usa
`linux_proc_vm_hwm_bytes` (`VmHWM` em `/proc/self/status`): é o high-water mark
cumulativo do processo, observado ao fim de cada cenário, não um pico isolado
por etapa nem uma medição de processo-filho.

## `testing.B` repetido

Os comandos foram executados cinco vezes, com memória e alocações:

```text
env GOCACHE=/tmp/manu-go-cache go test ./internal/benchmark -run '^$' \
  -bench 'Benchmark(DiscoveryHash|MetadataDigest|DirectoryBytes)$' \
  -benchmem -count=5
env GOCACHE=/tmp/manu-go-cache go test ./internal/analysis -run '^$' \
  -bench 'Benchmark(StateIndexLookup|InvalidatedPathsWithIndex)$' \
  -benchmem -count=5
```

Os valores `ns/op` de cada repetição foram:

| Benchmark | Cinco valores `ns/op` | Memória/alocações observadas |
| --- | --- | --- |
| `BenchmarkDiscoveryHash` | 818.307; 782.973; 790.627; 788.149; 789.569 | aproximadamente 414.258–414.457 B/op; 4.170–4.171 allocs/op |
| `BenchmarkMetadataDigest` | 97.719; 94.726; 95.170; 95.607; 96.485 | aproximadamente 25.274–25.284 B/op; 382 allocs/op |
| `BenchmarkDirectoryBytes` | 41.724; 41.595; 41.471; 41.652; 41.642 | aproximadamente 8.660–8.664 B/op; 108 allocs/op |
| `BenchmarkStateIndexLookup/entries=64` | 98,99; 97,30; 97,96; 97,87; 97,52 | 0 B/op; 0 allocs/op |
| `BenchmarkStateIndexLookup/entries=1024` | 105,3; 102,9; 103,1; 103,8; 104,1 | 0 B/op; 0 allocs/op |
| `BenchmarkStateIndexLookup/entries=16384` | 118,8; 119,7; 118,0; 119,7; 118,5 | 0 B/op; 0 allocs/op |
| `BenchmarkInvalidatedPathsWithIndex/artifacts=64` | 3.558; 3.518; 3.565; 3.525; 3.483 | 3.544 B/op; 4 allocs/op |
| `BenchmarkInvalidatedPathsWithIndex/artifacts=1024` | 70.191; 70.548; 70.282; 71.820; 70.851 | 54.656 B/op; 6 allocs/op |
| `BenchmarkInvalidatedPathsWithIndex/artifacts=16384` | 1.433.743; 1.434.593; 1.406.095; 1.386.046; 1.389.365 | aproximadamente 873.776 B/op; 66 allocs/op |

`BenchmarkDiscoveryHash` exercita o caminho real de descoberta, hashing e
classificação. Os benchmarks de índice cobrem lookup exato e invalidação
direta do estado por execução; o lookup permaneceu aproximadamente constante
(~96–121 ns/op) e sem alocações de 64 a 16.384 entradas. Os outros benchmarks
tornam visível o custo de medição do digest e do volume de saída. São evidências
locais para regressão do pipeline, não decisão isolada de runtime, capacidade
ou SLA.

## Cobertura, lacunas e limitações

- Ticketmaster cobriu o recorte autorizado Java/Quarkus com 70 artefatos; o
  analisador Java e o fallback genérico emitiram apenas estruturas e lacunas
  rastreáveis.
- A execução WSO2 abriu sem execução de conteúdo os seis CARs físicos
  selecionados e cobriu inventário declarativo limitado; não mede os 126 CARs
  excluídos.
- ERPNext cobriu 5.316 arquivos de `erpnext/**` para inventário/fallback
  genérico. Uma primeira medição exploratória revelou lookup/invalidação do
  estado incremental com comportamento O(n²); antes desta baseline aceita, o
  runner passou a construir um índice por execução. A tabela r4 mede o caminho
  corrigido; essa mudança e a medição exploratória permanecem registradas como
  evidência, sem ocultar a causa nem comparar os números obsoletos com a linha
  de base aceita.
- A atualização é uma simulação em staging temporário regular. O root da
  atualização é efêmero e removido ao terminar; o método não mede overlayfs nem
  copy-on-write do kernel.
- Bytes lidos não são todos os bytes de possíveis previews dos analisadores;
  volume persistido não é I/O físico; heap não é pico contínuo; MaxRSS é
  cumulativo. Métricas indisponíveis seriam declaradas, sem estimativa; neste
  Linux o MaxRSS esteve disponível.
- Não houve banco, persistência vetorial, consulta híbrida, embeddings, IA,
  tokens, custo, telemetria de execução de negócio ou comparação com outra
  stack. O resultado estático não é `Observed Execution`.

## Avaliação da decisão Go-first

Não houve falha material de segurança, integridade ou execução que exija
ajustar ou substituir a decisão Go-first: os três recortes concluíram, a
repetição preservou `EquivalentFacts`, a atualização foi localizada e todas as
fontes permaneceram inalteradas. A decisão permanece aceita para este
incremento, condicionada a novas medições quando persistência, recuperação,
semântica mais profunda ou outros runtimes entrarem em escopo. Esta baseline
não transforma o microcorte em comparação completa de stacks nem em SLA.
