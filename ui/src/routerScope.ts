/** Links the client-side router handles; /auth and /v1 are left to the browser and the server. */
export const ROUTER_SCOPE = /^\/(?!auth(?:\/|$)|v1(?:\/|$))/;
