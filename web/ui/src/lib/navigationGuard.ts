/**
 * A route-leave guard is a question the shell asks before it changes the
 * route: true means leaving is fine, false means the person declined.
 */
export type RouteLeaveGuard = () => boolean

export interface NavigationGuard {
  register(guard?: RouteLeaveGuard): void
  allow(): boolean
}

/**
 * One guard for the whole shell, so a view never has to know how a route
 * change happens. A view registers while it holds unsaved input and clears
 * itself when it is destroyed; the shell calls `allow()` exactly once per
 * navigation it initiates. With nothing registered, leaving is always fine.
 */
export function createNavigationGuard(): NavigationGuard {
  let active: RouteLeaveGuard | undefined
  return {
    register(guard) {
      active = guard
    },
    allow() {
      return active ? active() : true
    },
  }
}
