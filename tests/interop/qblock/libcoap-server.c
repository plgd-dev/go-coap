/* Application-only fixture. All Q-Block parsing, assembly, pacing, recovery and
 * packet encoding are performed by the unchanged pinned libcoap library. */
#include <coap3/coap.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static volatile sig_atomic_t stopping;
static void stop_handler(int sig) { (void)sig; stopping = 1; }
static void release_body(coap_session_t *session, void *arg) {
  (void)session;
  free(arg);
}

static void handle(coap_resource_t *resource, coap_session_t *session,
                   const coap_pdu_t *request, const coap_string_t *query,
                   coap_pdu_t *response) {
  size_t length = 1500, offset = 0, total = 0;
  const uint8_t *input = NULL;
  uint8_t *body;
  uint8_t size[4];
  int upload = coap_pdu_get_code(request) != COAP_REQUEST_CODE_GET;
  if (upload) {
    if (!coap_get_data_large(request, &length, &input, &offset, &total) ||
        offset != 0 || length != total) {
      coap_pdu_set_code(response, COAP_RESPONSE_CODE_INTERNAL_ERROR);
      return;
    }
  }
  body = malloc(length ? length : 1);
  if (!body) {
    coap_pdu_set_code(response, COAP_RESPONSE_CODE_INTERNAL_ERROR);
    return;
  }
  if (upload) memcpy(body, input, length);
  else for (size_t i = 0; i < length; i++) body[i] = (uint8_t)(33 + i % 90);
  coap_pdu_set_code(response, upload ? COAP_RESPONSE_CODE_CHANGED : COAP_RESPONSE_CODE_CONTENT);
  /* The shipped example's single-block probe omits this RFC9177-required
   * metadata. Supply the resource's real full length through the public API. */
  coap_add_option(response, COAP_OPTION_SIZE2,
                  coap_encode_var_safe(size, sizeof(size), (unsigned int)length), size);
  if (!coap_add_data_large_response(resource, session, request, response, query,
                                    COAP_MEDIATYPE_TEXT_PLAIN, -1, 1, length,
                                    body, release_body, body)) {
    free(body);
    coap_pdu_set_code(response, COAP_RESPONSE_CODE_INTERNAL_ERROR);
  }
  fprintf(stdout, "HANDLER %s %zu bytes\n", upload ? "upload" : "GET", length);
  fflush(stdout);
}

int main(int argc, char **argv) {
  coap_context_t *ctx;
  coap_address_t address;
  coap_resource_t *resource;
  int dtls = argc == 3 && strcmp(argv[2], "dtls") == 0;
  if (argc != 2 && !dtls) return 2;
  coap_startup();
  coap_set_log_level(COAP_LOG_DEBUG);
  ctx = coap_new_context(NULL);
  if (!ctx || !coap_q_block_is_supported()) return 2;
  coap_context_set_block_mode(ctx, COAP_BLOCK_USE_LIBCOAP | COAP_BLOCK_SINGLE_BODY | COAP_BLOCK_TRY_Q_BLOCK);
  coap_address_init(&address);
  address.addr.sin.sin_family = AF_INET;
  address.addr.sin.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
  address.addr.sin.sin_port = htons((uint16_t)atoi(argv[1]));
  if (dtls) {
    const uint8_t key[] = "qblock-interop-local-key";
    if (!coap_dtls_is_supported() || !coap_context_set_psk(ctx, "qblock-interop", key, sizeof(key) - 1)) return 2;
  }
  if (!coap_new_endpoint(ctx, &address, dtls ? COAP_PROTO_DTLS : COAP_PROTO_UDP)) return 2;
  resource = coap_resource_init(coap_make_str_const("fixture"), 0);
  coap_register_request_handler(resource, COAP_REQUEST_GET, handle);
  coap_register_request_handler(resource, COAP_REQUEST_POST, handle);
  coap_register_request_handler(resource, COAP_REQUEST_PUT, handle);
  coap_add_resource(ctx, resource);
  signal(SIGINT, stop_handler);
  signal(SIGTERM, stop_handler);
  printf("READY libcoap %s %s %s\n", coap_package_version(), dtls ? "DTLS" : "UDP", argv[1]);
  fflush(stdout);
  while (!stopping) if (coap_io_process(ctx, 100) < 0) break;
  coap_free_context(ctx);
  coap_cleanup();
  return 0;
}
