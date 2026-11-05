// Host-owned presentation is copied explicitly; tokens stay in the private file.
export function developmentUsers(tokens) {
  return Object.entries(tokens).map(([token, user]) => ({
    alias: token,
    label: user.label || user.subject,
    ...(user.description ? { description: user.description } : {}),
    ...(user.profileGroup !== undefined
      ? { profileGroup: user.profileGroup }
      : {}),
    ...(user.elevated !== undefined ? { elevated: user.elevated } : {}),
    ...(user.separateTenant !== undefined
      ? { separateTenant: user.separateTenant }
      : {}),
    token,
  }));
}
